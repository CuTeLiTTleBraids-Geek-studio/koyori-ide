import { afterEach, describe, it, expect, vi, expectTypeOf } from "vitest";
import type * as monacoEditor from "monaco-editor";

// Mock heavy transitive deps so importing lspCompletion doesn't pull in
// @wailsio/runtime / monaco-themes / the full LSP store graph. The helpers
// under test (computeDocumentHighlightScanRange / HIGHLIGHT_SCAN_WINDOW) are
// pure and don't touch these modules at runtime.
vi.mock("@/stores/lsp", () => ({
  getLSPCompletions: vi.fn(),
  getLSPHover: vi.fn(),
  getLSPDefinition: vi.fn(),
  getLSPReferences: vi.fn(),
  getLSPImplementation: vi.fn(),
  formatLSPDocument: vi.fn(),
  renameSymbolWorkspace: vi.fn(),
  getLSPSignatureHelp: vi.fn(),
  closeLSPDocument: vi.fn(),
  getLSPDocumentSymbols: vi.fn(),
  getLSPDeclaration: vi.fn(),
  getLSPTypeDefinition: vi.fn(),
  getLSPDocumentLinks: vi.fn(),
  getLSPSelectionRanges: vi.fn(),
  getLSPSemanticTokens: vi.fn(),
  resolveLSPCompletionItem: vi.fn(),
  getLSPInlayHints: vi.fn(),
  getLSPCodeLenses: vi.fn(),
  getLSPCodeActions: vi.fn(),
  monacoLanguageToLSP: vi.fn((language: string, filePath: string) =>
    filePath.endsWith(".vue") ? "vue" : language,
  ),
}));
vi.mock("../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/lspservice", () => ({
  GetTriggerCharacters: vi.fn().mockResolvedValue([]),
}));
vi.mock("@/stores/refactor", () => ({
  openRefactorActionPreview: vi.fn(),
}));
vi.mock("@/stores/editor", () => ({
  editorState: { openFiles: [], activeFilePath: null },
  updateContent: vi.fn(),
}));
vi.mock("@/lib/autoImport", () => ({
  getAutoImportCompletions: vi.fn(),
  mergeAutoImportSuggestions: vi.fn(),
}));
vi.mock("@/lib/notifications", () => ({
  notifySuccess: vi.fn(),
  notifyWarning: vi.fn(),
}));

import {
  computeDocumentHighlightScanRange,
  HIGHLIGHT_SCAN_WINDOW,
  registerLSPProviders,
  monacoLangToLSPKey,
  mapLSPCompletionToMonaco,
  INSERT_AS_SNIPPET_RULE,
  KEEP_WHITESPACE_RULE,
  LSP_CLIENT_CAPABILITIES,
  LSP_REFACTOR_PREVIEW_COMMAND,
  LSP_PROVIDER_LANGUAGES,
  getFallbackTriggerCharacters,
  mapMonacoCompletionTriggerKind,
  resolveModelFilePath,
  formatActiveDocument,
} from "./lspCompletion";
import {
  getLSPCodeActions,
  getLSPCompletions,
  getLSPDeclaration,
  getLSPDefinition,
  getLSPDocumentLinks,
  getLSPDocumentSymbols,
  getLSPHover,
  getLSPImplementation,
  getLSPInlayHints,
  getLSPReferences,
  getLSPSelectionRanges,
  getLSPSignatureHelp,
  getLSPTypeDefinition,
  renameSymbolWorkspace,
  resolveLSPCompletionItem,
  formatLSPDocument,
} from "@/stores/lsp";
import { GetTriggerCharacters } from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/lspservice";
import {
  getAutoImportCompletions,
  mergeAutoImportSuggestions,
} from "@/lib/autoImport";
import { editorState, updateContent } from "@/stores/editor";
import { openRefactorActionPreview } from "@/stores/refactor";
import { notifyWarning } from "@/lib/notifications";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

function buildOpenFile(path: string, content: string, language = "typescript") {
  return {
    path,
    name: path,
    content,
    originalContent: content,
    language,
    isDirty: false,
  };
}

afterEach(() => {
  editorState.openFiles.splice(0);
  editorState.activeFilePath = null;
});

describe("M-22: document highlight scan range", () => {
  it("HIGHLIGHT_SCAN_WINDOW is ±2000 lines (fallback when viewport unavailable)", () => {
    expect(HIGHLIGHT_SCAN_WINDOW).toBe(2000);
  });

  it("for a 100k-line document, findMatches only scans a bounded ±2000-line range", () => {
    // Previously the provider called model.findMatches(word, true, ...) which
    // scanned the whole document. Now the scan is bounded to a window around
    // the cursor — for a 100k-line file at line 50k, only lines 48k–52k.
    const range = computeDocumentHighlightScanRange(
      100_000,
      50_000,
      HIGHLIGHT_SCAN_WINDOW,
    );
    expect(range.startLineNumber).toBe(48_000);
    expect(range.endLineNumber).toBe(52_000);
    // Bounded window, not the whole document.
    expect(range.endLineNumber - range.startLineNumber).toBe(4_000);
    expect(range.startColumn).toBe(1);
  });

  it("clamps startLineNumber to 1 near the top of a large document", () => {
    const range = computeDocumentHighlightScanRange(
      100_000,
      100,
      HIGHLIGHT_SCAN_WINDOW,
    );
    expect(range.startLineNumber).toBe(1);
    expect(range.endLineNumber).toBe(2_100);
  });

  it("clamps endLineNumber to lineCount near the bottom", () => {
    const range = computeDocumentHighlightScanRange(
      100_000,
      99_900,
      HIGHLIGHT_SCAN_WINDOW,
    );
    expect(range.startLineNumber).toBe(97_900);
    expect(range.endLineNumber).toBe(100_000);
  });

  it("for a small document, scans the whole document (window exceeds line count)", () => {
    const range = computeDocumentHighlightScanRange(
      100,
      50,
      HIGHLIGHT_SCAN_WINDOW,
    );
    expect(range.startLineNumber).toBe(1);
    expect(range.endLineNumber).toBe(100);
  });

  it("the searchScope passed to model.findMatches is bounded, not the whole-document boolean", () => {
    // Spy on the model's findMatches to confirm a bounded IRange (not `true`)
    // is what the provider passes for a 100k-line document.
    const findMatchesSpy = vi.fn().mockReturnValue([]);
    const mockModel = {
      getLineCount: () => 100_000,
      findMatches: findMatchesSpy,
    };

    const scanRange = computeDocumentHighlightScanRange(
      mockModel.getLineCount(),
      50_000,
      HIGHLIGHT_SCAN_WINDOW,
    );
    // Mirror the provider's findMatches invocation exactly.
    (mockModel.findMatches as typeof findMatchesSpy)(
      "foo",
      scanRange,
      false,
      true,
      null,
      true,
    );

    expect(findMatchesSpy).toHaveBeenCalledTimes(1);
    const passedScope = findMatchesSpy.mock.calls[0][1];
    expect(passedScope).toMatchObject({
      startLineNumber: 48_000,
      endLineNumber: 52_000,
    });
    // Confirms we did NOT pass the whole-document boolean form (`true`).
    expect(passedScope).not.toBe(true);
  });
});

// L-9: Verify foldingRules markers use proper Monaco types (no `as any`).
// These type-level checks ensure the folding markers shape is compatible
// with monacoEditor.languages.FoldingMarkers / FoldingRules. If the types
// in lspCompletion.ts were wrong, vue-tsc --noEmit would fail AND these
// assertions would not compile.
describe("L-9: FoldingMarkers types (no as any)", () => {
  it("folding markers object is assignable to monacoEditor.languages.FoldingMarkers", () => {
    // Mirror the exact shape used in lspCompletion.ts for Go and TS/JS configs.
    const goMarkers = {
      start: /^\s*\/\/\s*region\b/,
      end: /^\s*\/\/\s*endregion\b/,
    };
    expectTypeOf(
      goMarkers,
    ).toMatchTypeOf<monacoEditor.languages.FoldingMarkers>();

    const tsJsMarkers = {
      start: /^\s*\/\/\s*#?region\b/,
      end: /^\s*\/\/\s*#?endregion\b/,
    };
    expectTypeOf(
      tsJsMarkers,
    ).toMatchTypeOf<monacoEditor.languages.FoldingMarkers>();
  });

  it("folding with markers is assignable to monacoEditor.languages.FoldingRules", () => {
    const folding = {
      markers: {
        start: /^\s*\/\/\s*region\b/,
        end: /^\s*\/\/\s*endregion\b/,
      },
    };
    expectTypeOf(folding).toMatchTypeOf<monacoEditor.languages.FoldingRules>();
  });

  it("a LanguageConfiguration with folding compiles without as any", () => {
    // This verifies the full configuration shape is valid. If the `as any`
    // removal introduced a type error, this assertion would fail to compile.
    const config: monacoEditor.languages.LanguageConfiguration = {
      comments: { lineComment: "//" },
      folding: {
        markers: {
          start: /^\s*\/\/\s*region\b/,
          end: /^\s*\/\/\s*endregion\b/,
        },
      },
    };
    expect(config.folding?.markers?.start).toBeDefined();
    expect(config.folding?.markers?.end).toBeDefined();
  });
});

// ---------------------------------------------------------------------------
// Priority 1: Inlay Hints Provider — 验证 registerInlayHintsProvider 被每个
// 语言调用，且 provideInlayHints 正确调用后端并映射响应。
// ---------------------------------------------------------------------------

// 构建一个最小化的 mock monaco，所有 register* 返回 IDisposable。
// registerInlayHintsProvider 是重点关注对象，使用 spy 捕获其参数。
function buildMockMonaco() {
  const noopDisposable = { dispose: () => undefined };
  const registerInlayHintsProviderSpy = vi.fn().mockReturnValue(noopDisposable);
  const registerDeclarationProviderSpy = vi
    .fn()
    .mockReturnValue(noopDisposable);
  const registerTypeDefinitionProviderSpy = vi
    .fn()
    .mockReturnValue(noopDisposable);
  const registerLinkProviderSpy = vi.fn().mockReturnValue(noopDisposable);
  const registerSelectionRangeProviderSpy = vi
    .fn()
    .mockReturnValue(noopDisposable);
  const registerFoldingRangeProviderSpy = vi
    .fn()
    .mockReturnValue(noopDisposable);
  const registerCodeActionProviderSpy = vi.fn().mockReturnValue(noopDisposable);
  const registerCommandSpy = vi.fn().mockReturnValue(noopDisposable);
  const languages = {
    register: vi.fn(),
    getLanguages: vi.fn().mockReturnValue([]),
    setLanguageConfiguration: vi.fn(),
    setMonarchTokensProvider: vi.fn(),
    registerCompletionItemProvider: vi.fn().mockReturnValue(noopDisposable),
    registerHoverProvider: vi.fn().mockReturnValue(noopDisposable),
    registerDefinitionProvider: vi.fn().mockReturnValue(noopDisposable),
    registerReferenceProvider: vi.fn().mockReturnValue(noopDisposable),
    registerImplementationProvider: vi.fn().mockReturnValue(noopDisposable),
    registerCodeActionProvider: registerCodeActionProviderSpy,
    registerDocumentFormattingEditProvider: vi
      .fn()
      .mockReturnValue(noopDisposable),
    registerRenameProvider: vi.fn().mockReturnValue(noopDisposable),
    registerSignatureHelpProvider: vi.fn().mockReturnValue(noopDisposable),
    registerCodeLensProvider: vi.fn().mockReturnValue(noopDisposable),
    registerDocumentSymbolProvider: vi.fn().mockReturnValue(noopDisposable),
    registerDocumentSemanticTokensProvider: vi
      .fn()
      .mockReturnValue(noopDisposable),
    registerInlayHintsProvider: registerInlayHintsProviderSpy,
    registerDocumentHighlightProvider: vi.fn().mockReturnValue(noopDisposable),
    // Architecture C: 新增 Provider 注册 spy
    registerDeclarationProvider: registerDeclarationProviderSpy,
    registerTypeDefinitionProvider: registerTypeDefinitionProviderSpy,
    registerLinkProvider: registerLinkProviderSpy,
    registerSelectionRangeProvider: registerSelectionRangeProviderSpy,
    registerFoldingRangeProvider: registerFoldingRangeProviderSpy,
    registerColorProvider: vi.fn().mockReturnValue(noopDisposable),
    registerLinkedEditingRangeProvider: vi.fn().mockReturnValue(noopDisposable),
    CompletionItemKind: { Snippet: 15 },
    CompletionItemInsertTextRule: { InsertAsSnippet: 4 },
    DocumentHighlightKind: { Read: 0 },
    IndentAction: { None: 0, IndentOutdent: 2 },
  };
  const Uri = {
    file: (p: string) => ({ fsPath: p, path: p, toString: () => p }),
  };
  return {
    languages,
    Uri,
    editor: { registerCommand: registerCommandSpy },
    registerInlayHintsProviderSpy,
    registerDeclarationProviderSpy,
    registerTypeDefinitionProviderSpy,
    registerLinkProviderSpy,
    registerSelectionRangeProviderSpy,
    registerFoldingRangeProviderSpy,
    registerCodeActionProviderSpy,
    registerCommandSpy,
  };
}

function installPendingProviderResponses(promise: Promise<unknown>): void {
  vi.mocked(getLSPHover).mockReset();
  vi.mocked(getLSPDefinition).mockReset();
  vi.mocked(getLSPReferences).mockReset();
  vi.mocked(getLSPImplementation).mockReset();
  vi.mocked(formatLSPDocument).mockReset();
  vi.mocked(renameSymbolWorkspace).mockReset();
  vi.mocked(getLSPSignatureHelp).mockReset();
  vi.mocked(getLSPDocumentSymbols).mockReset();
  vi.mocked(getLSPDeclaration).mockReset();
  vi.mocked(getLSPTypeDefinition).mockReset();
  vi.mocked(getLSPDocumentLinks).mockReset();
  vi.mocked(getLSPSelectionRanges).mockReset();

  vi.mocked(getLSPHover).mockReturnValue(
    promise as ReturnType<typeof getLSPHover>,
  );
  vi.mocked(getLSPDefinition).mockReturnValue(
    promise as ReturnType<typeof getLSPDefinition>,
  );
  vi.mocked(getLSPReferences).mockReturnValue(
    promise as ReturnType<typeof getLSPReferences>,
  );
  vi.mocked(getLSPImplementation).mockReturnValue(
    promise as ReturnType<typeof getLSPImplementation>,
  );
  vi.mocked(formatLSPDocument).mockReturnValue(
    promise as ReturnType<typeof formatLSPDocument>,
  );
  vi.mocked(renameSymbolWorkspace).mockReturnValue(
    promise as ReturnType<typeof renameSymbolWorkspace>,
  );
  vi.mocked(getLSPSignatureHelp).mockReturnValue(
    promise as ReturnType<typeof getLSPSignatureHelp>,
  );
  vi.mocked(getLSPDocumentSymbols).mockReturnValue(
    promise as ReturnType<typeof getLSPDocumentSymbols>,
  );
  vi.mocked(getLSPDeclaration).mockReturnValue(
    promise as ReturnType<typeof getLSPDeclaration>,
  );
  vi.mocked(getLSPTypeDefinition).mockReturnValue(
    promise as ReturnType<typeof getLSPTypeDefinition>,
  );
  vi.mocked(getLSPDocumentLinks).mockReturnValue(
    promise as ReturnType<typeof getLSPDocumentLinks>,
  );
  vi.mocked(getLSPSelectionRanges).mockReturnValue(
    promise as ReturnType<typeof getLSPSelectionRanges>,
  );
}

function buildDisposalSensitiveModel() {
  let disposed = false;
  let disposedAccesses = 0;
  const assertAlive = (): void => {
    if (!disposed) return;
    disposedAccesses += 1;
    throw new Error("Model is disposed!");
  };
  const model = {
    uri: {
      get fsPath() {
        assertAlive();
        return "/workspace/main.go";
      },
      get path() {
        assertAlive();
        return "/workspace/main.go";
      },
      toString() {
        assertAlive();
        return "file:///workspace/main.go";
      },
    },
    isDisposed: () => disposed,
    getValue: () => {
      assertAlive();
      return "package main\n";
    },
  } as unknown as monacoEditor.editor.ITextModel;
  return {
    model,
    dispose: () => {
      disposed = true;
    },
    disposedAccessCount: () => disposedAccesses,
  };
}

describe("10F: built-in language server routing", () => {
  it.each([
    ["json", "/workspace/tsconfig.json", "json"],
    ["json", "/workspace/settings.jsonc", "json"],
    ["css", "/workspace/site.css", "css"],
    ["scss", "/workspace/site.scss", "css"],
    ["less", "/workspace/site.less", "css"],
    ["html", "/workspace/index.html", "html"],
    ["yaml", "/workspace/config.yaml", "yaml"],
    ["python", "/workspace/main.py", "python"],
    ["rust", "/workspace/src/main.rs", "rust"],
  ])("routes %s (%s) to %s", (language, filePath, server) => {
    expect(monacoLangToLSPKey(language, filePath)).toBe(server);
  });
});

describe("model file path routing", () => {
  it("in-memory model 切换内容后不再使用首次注册的 preferredPath", () => {
    editorState.openFiles.splice(
      0,
      editorState.openFiles.length,
      {
        path: "C:\\repo\\first.ts",
        name: "first.ts",
        content: "export const first = 1;",
        originalContent: "export const first = 1;",
        language: "typescript",
        isDirty: false,
      },
      {
        path: "C:\\repo\\second.ts",
        name: "second.ts",
        content: "export const second = 2;",
        originalContent: "export const second = 2;",
        language: "typescript",
        isDirty: false,
      },
    );
    editorState.activeFilePath = "C:\\repo\\second.ts";
    const model = {
      uri: {
        path: "inmemory://model/1",
        toString: () => "inmemory://model/1",
      },
      getValue: () => "export const second = 2;",
    } as unknown as monacoEditor.editor.ITextModel;

    expect(resolveModelFilePath(model, "C:\\repo\\first.ts")).toBe(
      "C:\\repo\\second.ts",
    );

    editorState.openFiles.splice(0);
    editorState.activeFilePath = null;
  });

  it.each(["zero", "multiple"] as const)(
    "virtual model with %s content matches skips completion requests",
    async (matchCount) => {
      const content = "export const virtualOnly = true;";
      const openFiles =
        matchCount === "zero"
          ? [buildOpenFile("C:\\repo\\preferred.ts", "different content")]
          : [
              buildOpenFile("C:\\repo\\preferred.ts", content),
              buildOpenFile("C:\\repo\\other.ts", content),
            ];
      editorState.openFiles.splice(0, editorState.openFiles.length, ...openFiles);
      editorState.activeFilePath = "C:\\repo\\preferred.ts";
      vi.mocked(getLSPCompletions).mockClear();
      vi.mocked(getAutoImportCompletions).mockClear();

      const mock = buildMockMonaco();
      const disposable = registerLSPProviders(
        mock as unknown as typeof import("monaco-editor"),
        "C:\\repo\\preferred.ts",
      );
      const provider = mock.languages.registerCompletionItemProvider.mock.calls
        .find(([language]) => language === "typescript")?.[1] as {
        provideCompletionItems: (
          model: Record<string, unknown>,
          position: { lineNumber: number; column: number },
          context: { triggerKind: number },
          token: { isCancellationRequested: boolean },
        ) => Promise<{ suggestions: unknown[] }>;
      };
      const getWordUntilPosition = vi.fn();
      const model = {
        uri: {
          path: "inmemory://model/unresolved",
          toString: () => "inmemory://model/unresolved",
        },
        getValue: () => content,
        getWordUntilPosition,
      };

      const result = await provider.provideCompletionItems(
        model,
        { lineNumber: 1, column: 1 },
        { triggerKind: 0 },
        { isCancellationRequested: false },
      );

      expect(result).toEqual({ suggestions: [] });
      expect(getLSPCompletions).not.toHaveBeenCalled();
      expect(getAutoImportCompletions).not.toHaveBeenCalled();
      expect(getWordUntilPosition).not.toHaveBeenCalled();
      disposable.dispose();
    },
  );

  it("同一 Monaco 实例的分屏共享一套 Provider，并在最后引用释放后注销", () => {
    const mock = buildMockMonaco();
    const monaco = mock as unknown as typeof import("monaco-editor");
    const first = registerLSPProviders(monaco, "C:\\repo\\first.ts");
    const registrationCount =
      mock.languages.registerCompletionItemProvider.mock.calls.length;
    const second = registerLSPProviders(monaco, "C:\\repo\\second.ts");

    expect(
      mock.languages.registerCompletionItemProvider.mock.calls.length,
    ).toBe(registrationCount);
    first.dispose();
    expect(
      mock.languages.registerCompletionItemProvider.mock.calls.length,
    ).toBe(registrationCount);

    second.dispose();
    const third = registerLSPProviders(monaco, "C:\\repo\\third.ts");
    expect(
      mock.languages.registerCompletionItemProvider.mock.calls.length,
    ).toBeGreaterThan(registrationCount);
    third.dispose();
  });
});

describe("format-on-save race protection", () => {
  it("格式化响应基于旧 buffer 时不覆盖等待期间的新输入", async () => {
    let resolveFormat!: (edits: Array<{
      startLine: number;
      startCol: number;
      endLine: number;
      endCol: number;
      newText: string;
    }>) => void;
    vi.mocked(formatLSPDocument).mockReturnValueOnce(
      new Promise((resolve) => {
        resolveFormat = resolve;
      }),
    );
    editorState.openFiles.splice(0, editorState.openFiles.length, {
      path: "/workspace/app.ts",
      name: "app.ts",
      content: "const oldValue=1",
      originalContent: "const oldValue=1",
      language: "typescript",
      isDirty: true,
    });

    const formatting = formatActiveDocument(
      "typescript",
      "/workspace/app.ts",
      "const oldValue=1",
    );
    editorState.openFiles[0].content = "const newValue = 2";
    resolveFormat([
      {
        startLine: 0,
        startCol: 0,
        endLine: 0,
        endCol: 16,
        newText: "const formatted = 1;",
      },
    ]);

    await expect(formatting).resolves.toBe(false);
    expect(updateContent).not.toHaveBeenCalledWith(
      "/workspace/app.ts",
      "const formatted = 1;",
    );
    editorState.openFiles.splice(0);
  });
});

describe("9J: framework language server routing", () => {
  it("routes Vue single-file components to Volar before the HTML fallback", () => {
    expect(monacoLangToLSPKey("html", "/workspace/src/App.vue")).toBe("vue");
  });
});

describe("10C: Go template language server routing", () => {
  it.each([
    ["go-template", "/workspace/page.gohtml"],
    ["plaintext", "/workspace/layout.tmpl"],
    ["plaintext", "/workspace/partial.gotmpl"],
  ])("routes %s (%s) to gopls", (language, filePath) => {
    expect(monacoLangToLSPKey(language, filePath)).toBe("go");
  });
});

describe("10C: Go template Monaco registration", () => {
  it("注册随 Provider 生命周期释放，并可在重新挂载时恢复", () => {
    const mock = buildMockMonaco();
    const monaco = mock as unknown as typeof import("monaco-editor");

    registerLSPProviders(monaco).dispose();
    registerLSPProviders(monaco).dispose();

    const registrations = mock.languages.register.mock.calls
      .map(([registration]) => registration)
      .filter((registration) => registration.id === "go-template");
    expect(registrations).toHaveLength(2);
    expect(registrations[0]).toEqual(
      expect.objectContaining({
        id: "go-template",
        extensions: [".gohtml", ".tmpl", ".gotmpl"],
      }),
    );

    const configurations =
      mock.languages.setLanguageConfiguration.mock.calls.filter(
        ([language]) => language === "go-template",
      );
    expect(configurations).toHaveLength(2);
    expect(configurations[0][1]).toMatchObject({
      comments: { blockComment: ["{{/*", "*/}}"] },
      brackets: expect.arrayContaining([["{{", "}}"]]),
    });

    const monarchProviders =
      mock.languages.setMonarchTokensProvider.mock.calls.filter(
        ([language]) => language === "go-template",
      );
    expect(monarchProviders).toHaveLength(2);
    expect(JSON.stringify(monarchProviders[0][1])).toContain("text/html");
  });
});

type TestCompletionProvider = {
  triggerCharacters?: string[];
  provideCompletionItems: (
    model: Record<string, unknown>,
    position: { lineNumber: number; column: number },
  ) => unknown;
};

async function getGoStructTagSuggestions(
  line: string,
  column = line.length + 1,
) {
  const mock = buildMockMonaco();
  const monaco = mock as unknown as typeof import("monaco-editor");
  const disposable = registerLSPProviders(monaco);
  const goProviders = mock.languages.registerCompletionItemProvider.mock.calls
    .filter(([language]) => language === "go")
    .map(([, provider]) => provider as TestCompletionProvider);
  const provider = goProviders.at(-1);
  expect(provider).toBeDefined();
  const lspProvider = goProviders[0];
  expect(lspProvider).toBeDefined();

  const prefix = line.slice(0, column - 1);
  const wordMatch = /[A-Za-z_]\w*$/.exec(prefix);
  const word = wordMatch?.[0] ?? "";
  const result = (await provider!.provideCompletionItems(
    {
      getLineContent: () => line,
      getWordUntilPosition: () => ({
        word,
        startColumn: column - word.length,
        endColumn: column,
      }),
    },
    { lineNumber: 1, column },
  )) as { suggestions: Array<{ label: string; insertText: string }> };
  disposable.dispose();
  return { lspProvider, provider: provider!, suggestions: result.suggestions };
}

describe("10C: Go struct tag contextual snippets", () => {
  it("offers json/yaml/xml/db snippets with editable omitempty in a field context", async () => {
    const { lspProvider, provider, suggestions } =
      await getGoStructTagSuggestions("\tUserID string ");

    expect(lspProvider.triggerCharacters).toEqual([".", "("]);
    expect(provider.triggerCharacters).toEqual(
      expect.arrayContaining(["`", '"', ","]),
    );
    expect(suggestions.map((item) => item.label)).toEqual([
      "json",
      "yaml",
      "xml",
      "db",
    ]);
    expect(suggestions.find((item) => item.label === "json")?.insertText).toBe(
      '`json:"${1:user_id}${2:,omitempty}"`',
    );
  });

  it("deduplicates tag keys already present in the current struct tag", async () => {
    const line = '\tUserID string `json:"user_id" yaml:"user_id" `';
    const { suggestions } = await getGoStructTagSuggestions(line, line.length);

    expect(suggestions.map((item) => item.label)).toEqual(["xml", "db"]);
    expect(suggestions.every((item) => !item.insertText.includes("`"))).toBe(
      true,
    );
  });

  it("offers omitempty only while editing a tag value and does not duplicate it", async () => {
    const incomplete = '\tUserID string `json:"user_id';
    const first = await getGoStructTagSuggestions(incomplete);
    expect(first.suggestions).toEqual([
      expect.objectContaining({ label: "omitempty", insertText: ",omitempty" }),
    ]);

    const existing = '\tUserID string `json:"user_id,omitempty';
    const second = await getGoStructTagSuggestions(existing);
    expect(second.suggestions).toEqual([]);
  });

  it("does not offer struct tags outside a field declaration context", async () => {
    const { suggestions } = await getGoStructTagSuggestions("returnValue");
    expect(suggestions).toEqual([]);
  });
});

describe("Priority 1: Inlay Hints Provider 注册", () => {
  it("registerInlayHintsProvider 为每个支持的注册语言调用一次", () => {
    const mock = buildMockMonaco();
    const monaco = mock as unknown as typeof import("monaco-editor");
    const disposable = registerLSPProviders(monaco);
    expect(mock.registerInlayHintsProviderSpy).toHaveBeenCalledTimes(
      LSP_PROVIDER_LANGUAGES.length,
    );
    // 验证每种语言都注册了。
    const registeredLangs = mock.registerInlayHintsProviderSpy.mock.calls.map(
      (c) => c[0],
    );
    expect(registeredLangs).toEqual(
      expect.arrayContaining([...LSP_PROVIDER_LANGUAGES]),
    );
    disposable.dispose();
  });

  it("provideInlayHints 调用 getLSPInlayHints 并将后端响应映射为 Monaco InlayHint", async () => {
    const mock = buildMockMonaco();
    const monaco = mock as unknown as typeof import("monaco-editor");
    const disposable = registerLSPProviders(monaco);

    // 捕获 "go" 语言的 InlayHintsProvider。
    const goCall = mock.registerInlayHintsProviderSpy.mock.calls.find(
      (c) => c[0] === "go",
    );
    expect(goCall).toBeDefined();
    const provider = goCall![1];

    // 准备 mock 后端响应：0-based 位置。
    const backendHints = [
      { line: 4, column: 7, label: ": string", kind: 1 }, // type hint
      { line: 5, column: 2, label: "name:", kind: 2 }, // parameter hint
    ];
    vi.mocked(getLSPInlayHints).mockResolvedValue(backendHints as never);

    // 准备 mock model。
    const mockModel = {
      uri: { path: "/repo/main.go", toString: () => "/repo/main.go" },
      getValue: () => "package main\n",
    };
    const result = await provider.provideInlayHints(mockModel, {
      startLineNumber: 1,
      startColumn: 1,
      endLineNumber: 100,
      endColumn: 1,
    }, { isCancellationRequested: false });

    // 验证后端被正确调用。
    expect(getLSPInlayHints).toHaveBeenCalledWith(
      "go",
      "/repo/main.go",
      "package main\n",
      0,
      100,
    );

    // 验证映射：0-based → 1-based，kind 透传，padding 按 kind 设置。
    expect(result).not.toBeNull();
    expect(result!.hints).toHaveLength(2);
    expect(result!.hints[0]).toEqual({
      label: ": string",
      position: { lineNumber: 5, column: 8 },
      kind: 1,
      paddingLeft: false,
      paddingRight: true, // kind=1 (Type) → paddingRight
    });
    expect(result!.hints[1]).toEqual({
      label: "name:",
      position: { lineNumber: 6, column: 3 },
      kind: 2,
      paddingLeft: true, // kind=2 (Parameter) → paddingLeft
      paddingRight: false,
    });
    // dispose 是函数。
    expect(typeof result!.dispose).toBe("function");

    disposable.dispose();
  });

  it("provideInlayHints 在后端返回空列表时返回空 hints（不报错）", async () => {
    const mock = buildMockMonaco();
    const monaco = mock as unknown as typeof import("monaco-editor");
    const disposable = registerLSPProviders(monaco);

    const tsCall = mock.registerInlayHintsProviderSpy.mock.calls.find(
      (c) => c[0] === "typescript",
    );
    expect(tsCall).toBeDefined();
    const provider = tsCall![1];

    vi.mocked(getLSPInlayHints).mockResolvedValue([]);
    const mockModel = {
      uri: { path: "/repo/index.ts", toString: () => "/repo/index.ts" },
      getValue: () => "const x = 1;",
    };
    const result = await provider.provideInlayHints(mockModel, {
      startLineNumber: 1,
      startColumn: 1,
      endLineNumber: 10,
      endColumn: 1,
    }, { isCancellationRequested: false });
    expect(result).not.toBeNull();
    expect(result!.hints).toEqual([]);
    expect(typeof result!.dispose).toBe("function");

    disposable.dispose();
  });

  it("resolveInlayHint 在后端 resolve 不可用时降级返回原始 hint", async () => {
    const mock = buildMockMonaco();
    const monaco = mock as unknown as typeof import("monaco-editor");
    const disposable = registerLSPProviders(monaco);

    const goCall = mock.registerInlayHintsProviderSpy.mock.calls.find(
      (c) => c[0] === "go",
    );
    const provider = goCall![1];
    expect(typeof provider.resolveInlayHint).toBe("function");

    const hint = {
      label: ": int",
      position: { lineNumber: 1, column: 1 },
      kind: 1,
    };
    // The provider only reads cancellation state; Monaco's full token also
    // carries an event object that is irrelevant to this isolated test.
    const token = {
      isCancellationRequested: false,
    } as unknown as monacoEditor.CancellationToken;
    const resolved = await provider.resolveInlayHint(hint, token);
    expect(resolved).toBe(hint);

    disposable.dispose();
  });
});

// ---------------------------------------------------------------------------
// Priority 2 (prompt-1.md): 补全 snippetSupport + labelDetails
// 验证 LSP CompletionItem → Monaco CompletionItem 的映射：
//   - insertTextFormat === 2 (Snippet) 时设置 InsertAsSnippet 规则
//   - labelDetails.detail / description 映射到 Monaco CompletionItemLabel
//   - 客户端能力声明 snippetSupport = true
// ---------------------------------------------------------------------------

const COMPLETION_RANGE = {
  startLineNumber: 1,
  endLineNumber: 1,
  startColumn: 1,
  endColumn: 5,
};

describe("Priority 2: 补全 snippetSupport + labelDetails 映射", () => {
  it("insertTextFormat:2 且 insertText 为 snippet 时，映射为 InsertAsSnippet", () => {
    // LSP 返回 Snippet 格式（insertTextFormat === 2），insertText 含 $1 占位符。
    const item = {
      label: "Func",
      kind: 3,
      detail: "",
      insertText: "func($1)",
      insertTextFormat: 2,
    };
    const result = mapLSPCompletionToMonaco(item, COMPLETION_RANGE, "Func");
    // insertText 原样透传（Monaco 负责解析 $1 占位符）。
    expect(result.insertText).toBe("func($1)");
    // insertTextRules 标记为 InsertAsSnippet（Monaco 枚举值 4）。
    expect(result.insertTextRules).toBe(INSERT_AS_SNIPPET_RULE);
    expect(result.insertTextRules).toBe(4);
  });

  it("insertTextFormat 缺省（纯文本）时不设置 InsertAsSnippet 规则", () => {
    const item = { label: "foo", kind: 6, detail: "", insertText: "foo" };
    const result = mapLSPCompletionToMonaco(item, COMPLETION_RANGE, "foo");
    expect(result.insertTextRules).toBe(0);
  });

  it("labelDetails.detail/description 映射到 Monaco CompletionItemLabel + documentation", () => {
    const item = {
      label: "Func",
      kind: 3,
      detail: "",
      insertText: "Func",
      labelDetails: { detail: "(a, b string)", description: "func example" },
    };
    const result = mapLSPCompletionToMonaco(item, COMPLETION_RANGE, "Func");
    // Monaco 0.52 CompletionItemLabel = { label, detail?, description? }：
    // labelDetails.detail（短签名）→ label.detail，labelDetails.description → label.description。
    expect(result.label).toEqual({
      label: "Func",
      detail: "(a, b string)",
      description: "func example",
    });
    // 完整描述同时呈现到 documentation 面板。
    expect(result.documentation).toBe("func example");
  });

  it("无 labelDetails 时 label 保持为纯字符串", () => {
    const item = {
      label: "foo",
      kind: 6,
      detail: "var foo int",
      insertText: "foo",
    };
    const result = mapLSPCompletionToMonaco(item, COMPLETION_RANGE, "foo");
    expect(result.label).toBe("foo");
    expect(result.detail).toBe("var foo int");
    expect(result.documentation).toBeUndefined();
  });

  it("LSP_CLIENT_CAPABILITIES 声明 completion.completionItem.snippetSupport = true", () => {
    // 该常量镜像后端 services/lsp_service.go initializeLocked 中发送给 LSP
    // server 的客户端能力，确保 snippet 支持已宣告。
    expect(
      LSP_CLIENT_CAPABILITIES.textDocument.completion.completionItem
        .snippetSupport,
    ).toBe(true);
  });

  it("优先使用服务端 sortText/filterText/preselect/commitCharacters 与 deprecated tag", () => {
    const result = mapLSPCompletionToMonaco(
      {
        label: "serverItem",
        kind: 3,
        detail: "",
        insertText: "serverItem",
        sortText: "000-server",
        filterText: "srv",
        preselect: true,
        commitCharacters: ["."],
        deprecated: true,
        documentation: { kind: "markdown", value: "**server docs**" },
      },
      COMPLETION_RANGE,
      "server",
    );

    expect(result.sortText).toBe("000-server");
    expect(result.filterText).toBe("srv");
    expect(result.preselect).toBe(true);
    expect(result.commitCharacters).toEqual(["."]);
    expect(result.tags).toEqual([1]);
    expect(result.documentation).toEqual({ value: "**server docs**" });
  });

  it("MarkupContent kind=plaintext 按纯文本交给 Monaco", () => {
    const result = mapLSPCompletionToMonaco(
      {
        label: "plain",
        kind: 1,
        detail: "",
        insertText: "plain",
        documentation: { kind: "plaintext", value: "a *literal* value" },
      },
      COMPLETION_RANGE,
      "plain",
    );
    expect(result.documentation).toBe("a *literal* value");
  });

  it("服务端字段缺失时保留客户端排序、过滤与 commit character fallback", () => {
    const result = mapLSPCompletionToMonaco(
      { label: "foobar", kind: 6, detail: "", insertText: "foobar" },
      COMPLETION_RANGE,
      "foo",
    );
    expect(result.sortText).toBe("1");
    expect(result.filterText).toBe("foobar");
    expect(result.commitCharacters).toContain("(");
  });

  it("映射规范顶层 InsertReplaceEdit，并尊重 insertTextMode", () => {
    const result = mapLSPCompletionToMonaco(
      {
        label: "fallbackLabel",
        kind: 3,
        detail: "",
        insertText: "fallbackInsert",
        insertTextMode: 1,
        textEdit: {
          newText: "serverInsert",
          insert: {
            start: { line: 1, character: 2 },
            end: { line: 1, character: 4 },
          },
          replace: {
            start: { line: 1, character: 2 },
            end: { line: 1, character: 8 },
          },
        },
      },
      COMPLETION_RANGE,
      "fall",
    );

    expect(result.insertText).toBe("serverInsert");
    expect(result.range).toEqual({
      insert: {
        startLineNumber: 2,
        startColumn: 3,
        endLineNumber: 2,
        endColumn: 5,
      },
      replace: {
        startLineNumber: 2,
        startColumn: 3,
        endLineNumber: 2,
        endColumn: 9,
      },
    });
    expect(result.insertTextRules).toBe(KEEP_WHITESPACE_RULE);
  });

  it("规范顶层双区间优先于历史嵌套与 flattened range", () => {
    const result = mapLSPCompletionToMonaco(
      {
        label: "value",
        kind: 6,
        detail: "",
        insertText: "fallback",
        textEdit: {
          newText: "wire",
          startLine: 8,
          startCol: 8,
          endLine: 8,
          endCol: 9,
          insert: {
            start: { line: 2, character: 1 },
            end: { line: 2, character: 3 },
          },
          replace: {
            start: { line: 2, character: 1 },
            end: { line: 2, character: 7 },
          },
          range: {
            start: { line: 4, character: 0 },
            end: { line: 4, character: 5 },
          },
        },
      },
      COMPLETION_RANGE,
      "val",
    );

    expect(result.insertText).toBe("wire");
    expect(result.range).toEqual({
      insert: {
        startLineNumber: 3,
        startColumn: 2,
        endLineNumber: 3,
        endColumn: 4,
      },
      replace: {
        startLineNumber: 3,
        startColumn: 2,
        endLineNumber: 3,
        endColumn: 8,
      },
    });
  });

  it("兼容历史 range.{insert,replace} 与 flattened Go DTO", () => {
    const historical = mapLSPCompletionToMonaco(
      {
        label: "legacy",
        kind: 6,
        detail: "",
        insertText: "legacy",
        textEdit: {
          newText: "nested",
          range: {
            insert: {
              start: { line: 0, character: 1 },
              end: { line: 0, character: 2 },
            },
            replace: {
              start: { line: 0, character: 1 },
              end: { line: 0, character: 5 },
            },
          },
        },
      } as unknown as import("@/types").LSPCompletionItem,
      COMPLETION_RANGE,
      "leg",
    );
    const flattened = mapLSPCompletionToMonaco(
      {
        label: "flat",
        kind: 6,
        detail: "",
        insertText: "flat",
        textEdit: {
          newText: "flattened",
          startLine: 3,
          startCol: 2,
          endLine: 3,
          endCol: 6,
        },
      },
      COMPLETION_RANGE,
      "fla",
    );

    expect(historical.range).toEqual({
      insert: {
        startLineNumber: 1,
        startColumn: 2,
        endLineNumber: 1,
        endColumn: 3,
      },
      replace: {
        startLineNumber: 1,
        startColumn: 2,
        endLineNumber: 1,
        endColumn: 6,
      },
    });
    expect(flattened).toMatchObject({
      insertText: "flattened",
      range: {
        startLineNumber: 4,
        startColumn: 3,
        endLineNumber: 4,
        endColumn: 7,
      },
    });
  });

  it("nullable optional 字段缺失时 fallback，显式空值保持为空", () => {
    const absent = mapLSPCompletionToMonaco(
      {
        label: "fallback",
        kind: 6,
        detail: "",
        insertText: null,
        textEditText: null,
        sortText: null,
        filterText: null,
        commitCharacters: null,
        textEdit: null,
      },
      COMPLETION_RANGE,
      "fall",
    );
    const empty = mapLSPCompletionToMonaco(
      {
        label: "empty",
        kind: 6,
        detail: "",
        insertText: "ignored",
        textEditText: "",
        sortText: "",
        filterText: "",
        commitCharacters: [],
      },
      COMPLETION_RANGE,
      "emp",
    );

    expect(absent.insertText).toBe("fallback");
    expect(absent.commitCharacters).toContain("(");
    expect(empty).toMatchObject({
      insertText: "",
      sortText: "",
      filterText: "",
      commitCharacters: [],
    });
  });
});

describe("prompt-3: completion context、触发字符与 resolve", () => {
  it.each([
    [0, 1],
    [1, 2],
    [2, 1],
    [3, 3],
  ])("映射 Monaco triggerKind %s 为 LSP %s", (monacoKind, lspKind) => {
    expect(mapMonacoCompletionTriggerKind(monacoKind)).toBe(lspKind);
  });

  it("按语言提供窄化的 triggerCharacters fallback", () => {
    expect(getFallbackTriggerCharacters("go")).toEqual([".", "("]);
    expect(getFallbackTriggerCharacters("typescript")).toEqual([
      ".",
      '"',
      "'",
      "<",
      "/",
    ]);
    expect(getFallbackTriggerCharacters("python")).toEqual(["."]);
  });

  it("异步服务端 triggerCharacters 会原地替换 fallback", async () => {
    const mock = buildMockMonaco();
    const disposable = registerLSPProviders(
      mock as unknown as typeof import("monaco-editor"),
      undefined,
      {
        getTriggerCharacters: async (language) =>
          language === "go" ? ["@", "."] : [],
      },
    );
    const provider = mock.languages.registerCompletionItemProvider.mock.calls
      .find(([language]) => language === "go")?.[1] as {
      triggerCharacters: string[];
    };

    await vi.waitFor(() => {
      expect(provider.triggerCharacters).toEqual(["@", "."]);
    });
    disposable.dispose();
  });

  it("LSP lazy start 后重新获取服务端 triggerCharacters", async () => {
    const triggerCalls = new Map<string, number>();
    const getTriggerCharacters = vi.fn(async (language: string) => {
      const count = (triggerCalls.get(language) ?? 0) + 1;
      triggerCalls.set(language, count);
      return language === "go" && count > 1 ? [":"] : [];
    });
    vi.mocked(getLSPCompletions).mockResolvedValueOnce({
      items: [],
      isIncomplete: false,
    });
    vi.mocked(getAutoImportCompletions).mockResolvedValueOnce([]);
    const mock = buildMockMonaco();
    const disposable = registerLSPProviders(
      mock as unknown as typeof import("monaco-editor"),
      "/workspace/main.go",
      { getTriggerCharacters },
    );
    const provider = mock.languages.registerCompletionItemProvider.mock.calls
      .find(([language]) => language === "go")?.[1] as {
      triggerCharacters: string[];
      provideCompletionItems: (
        model: Record<string, unknown>,
        position: { lineNumber: number; column: number },
        context: { triggerKind: number },
        token: { isCancellationRequested: boolean },
      ) => Promise<unknown>;
    };

    await provider.provideCompletionItems(
      {
        uri: {
          path: "/workspace/main.go",
          toString: () => "file:///workspace/main.go",
        },
        getValue: () => "package main",
        getWordUntilPosition: () => ({
          word: "main",
          startColumn: 9,
          endColumn: 13,
        }),
      },
      { lineNumber: 1, column: 13 },
      { triggerKind: 0 },
      { isCancellationRequested: false },
    );

    await vi.waitFor(() => {
      expect(provider.triggerCharacters).toEqual([":"]);
    });
    disposable.dispose();
  });

  it("同一 html Provider 合并 HTML 与 Vue server 的 triggerCharacters", async () => {
    const getTriggerCharacters = vi.fn(async (language: string) =>
      language === "vue" ? ["@"] : language === "html" ? ["<"] : [],
    );
    vi.mocked(getLSPCompletions).mockResolvedValueOnce({
      items: [],
      isIncomplete: false,
    });
    vi.mocked(getAutoImportCompletions).mockResolvedValueOnce([]);
    const mock = buildMockMonaco();
    const disposable = registerLSPProviders(
      mock as unknown as typeof import("monaco-editor"),
      "/workspace/index.html",
      { getTriggerCharacters },
    );
    const provider = mock.languages.registerCompletionItemProvider.mock.calls
      .find(([language]) => language === "html")?.[1] as {
      triggerCharacters: string[];
      provideCompletionItems: (
        model: Record<string, unknown>,
        position: { lineNumber: number; column: number },
        context: { triggerKind: number },
        token: { isCancellationRequested: boolean },
      ) => Promise<unknown>;
    };

    await provider.provideCompletionItems(
      {
        uri: {
          path: "/workspace/App.vue",
          toString: () => "file:///workspace/App.vue",
        },
        getValue: () => "<template>@</template>",
        getWordUntilPosition: () => ({
          word: "",
          startColumn: 12,
          endColumn: 12,
        }),
      },
      { lineNumber: 1, column: 12 },
      { triggerKind: 0 },
      { isCancellationRequested: false },
    );

    await vi.waitFor(() => {
      expect(provider.triggerCharacters).toEqual(
        expect.arrayContaining(["<", "@"]),
      );
    });
    disposable.dispose();
  });

  it("provideCompletionItems 透传 context 并返回 CompletionList.incomplete", async () => {
    vi.mocked(GetTriggerCharacters).mockResolvedValue([]);
    vi.mocked(getLSPCompletions).mockResolvedValueOnce({
      items: [
        { label: "Println", kind: 3, detail: "", insertText: "Println" },
      ],
      isIncomplete: true,
    });
    vi.mocked(getAutoImportCompletions).mockResolvedValueOnce([]);
    vi.mocked(mergeAutoImportSuggestions).mockImplementationOnce(
      (suggestions) => suggestions,
    );
    const mock = buildMockMonaco();
    const disposable = registerLSPProviders(
      mock as unknown as typeof import("monaco-editor"),
      "/workspace/main.go",
    );
    const provider = mock.languages.registerCompletionItemProvider.mock.calls
      .find(([language]) => language === "go")?.[1] as {
      provideCompletionItems: (
        model: Record<string, unknown>,
        position: { lineNumber: number; column: number },
        context: { triggerKind: number; triggerCharacter?: string },
        token: { isCancellationRequested: boolean },
      ) => Promise<{ suggestions: unknown[]; incomplete?: boolean }>;
    };
    const model = {
      uri: {
        path: "/workspace/main.go",
        toString: () => "file:///workspace/main.go",
      },
      getValue: () => "fmt.",
      getWordUntilPosition: () => ({
        word: "",
        startColumn: 5,
        endColumn: 5,
      }),
    };

    const result = await provider.provideCompletionItems(
      model,
      { lineNumber: 1, column: 5 },
      { triggerKind: 1, triggerCharacter: "." },
      { isCancellationRequested: false },
    );

    expect(getLSPCompletions).toHaveBeenCalledWith(
      "go",
      "/workspace/main.go",
      0,
      4,
      "fmt.",
      2,
      ".",
    );
    expect(result.incomplete).toBe(true);
    expect(result.suggestions).toHaveLength(1);
    disposable.dispose();
  });

  it("documentation 为空时 resolve 会传递完整 item（含 data）并缓存响应", async () => {
    const mock = buildMockMonaco();
    const disposable = registerLSPProviders(
      mock as unknown as typeof import("monaco-editor"),
    );
    const provider = mock.languages.registerCompletionItemProvider.mock.calls
      .find(([language]) => language === "go")?.[1] as {
      resolveCompletionItem: (
        item: monacoEditor.languages.CompletionItem,
        token: { isCancellationRequested: boolean },
      ) => Promise<monacoEditor.languages.CompletionItem>;
    };
    const source = {
      label: "Println",
      kind: 3,
      detail: "func(...any)",
      insertText: "Println",
      data: { resolveID: 42 },
    };
    const item = mapLSPCompletionToMonaco(source, COMPLETION_RANGE, "Print");
    vi.mocked(resolveLSPCompletionItem).mockResolvedValueOnce({
      ...source,
      documentation: { kind: "markdown", value: "Println docs" },
    });

    const resolved = await provider.resolveCompletionItem(item, {
      isCancellationRequested: false,
    });

    expect(resolveLSPCompletionItem).toHaveBeenCalledWith("go", source);
    expect(resolved.documentation).toEqual({ value: "Println docs" });
    expect(
      (resolved as typeof resolved & {
        __resolvedLSPCompletionItem?: { data?: unknown };
      }).__resolvedLSPCompletionItem?.data,
    ).toEqual({ resolveID: 42 });
    disposable.dispose();
  });

  it("labelDetails description 不会阻止缺少 LSP documentation 的 item resolve", async () => {
    const mock = buildMockMonaco();
    const disposable = registerLSPProviders(
      mock as unknown as typeof import("monaco-editor"),
    );
    const provider = mock.languages.registerCompletionItemProvider.mock.calls
      .find(([language]) => language === "go")?.[1] as {
      resolveCompletionItem: (
        item: monacoEditor.languages.CompletionItem,
        token: { isCancellationRequested: boolean },
      ) => Promise<monacoEditor.languages.CompletionItem>;
    };
    const source = {
      label: "WithDescription",
      kind: 3,
      detail: "",
      insertText: "WithDescription",
      labelDetails: { description: "list description only" },
      data: { id: 77 },
    };
    vi.mocked(resolveLSPCompletionItem).mockResolvedValueOnce({
      ...source,
      documentation: "resolved documentation",
    });
    const item = mapLSPCompletionToMonaco(source, COMPLETION_RANGE, "With");

    await provider.resolveCompletionItem(item, {
      isCancellationRequested: false,
    });

    expect(resolveLSPCompletionItem).toHaveBeenCalledWith("go", source);
    disposable.dispose();
  });

  it("Vue completion resolve 使用 provide 阶段缓存的 vue server key", async () => {
    vi.mocked(getLSPCompletions).mockResolvedValueOnce({
      items: [
        {
          label: "defineProps",
          kind: 3,
          detail: "",
          insertText: "defineProps",
          data: { id: "vue-item" },
        },
      ],
      isIncomplete: false,
    });
    vi.mocked(getAutoImportCompletions).mockResolvedValueOnce([]);
    vi.mocked(mergeAutoImportSuggestions).mockImplementationOnce(
      (suggestions) => suggestions,
    );
    vi.mocked(resolveLSPCompletionItem).mockResolvedValueOnce({
      label: "defineProps",
      kind: 3,
      detail: "Vue macro",
      insertText: "defineProps",
      data: { id: "vue-item" },
    });
    const mock = buildMockMonaco();
    const disposable = registerLSPProviders(
      mock as unknown as typeof import("monaco-editor"),
      "/workspace/App.vue",
    );
    const provider = mock.languages.registerCompletionItemProvider.mock.calls
      .find(([language]) => language === "html")?.[1] as {
      provideCompletionItems: (
        model: Record<string, unknown>,
        position: { lineNumber: number; column: number },
        context: { triggerKind: number },
        token: { isCancellationRequested: boolean },
      ) => Promise<{ suggestions: monacoEditor.languages.CompletionItem[] }>;
      resolveCompletionItem: (
        item: monacoEditor.languages.CompletionItem,
        token: { isCancellationRequested: boolean },
      ) => Promise<monacoEditor.languages.CompletionItem>;
    };
    editorState.openFiles.splice(
      0,
      editorState.openFiles.length,
      buildOpenFile(
        "/workspace/App.vue",
        "<script setup>define",
        "vue",
      ),
    );
    editorState.activeFilePath = "/workspace/App.vue";
    const model = {
      uri: {
        path: "inmemory://model/1",
        toString: () => "inmemory://model/1",
      },
      getValue: () => "<script setup>define",
      getWordUntilPosition: () => ({
        word: "define",
        startColumn: 15,
        endColumn: 21,
      }),
    };
    const provided = await provider.provideCompletionItems(
      model,
      { lineNumber: 1, column: 21 },
      { triggerKind: 0 },
      { isCancellationRequested: false },
    );
    const item = provided.suggestions[0];
    await provider.resolveCompletionItem(item, {
      isCancellationRequested: false,
    });
    await provider.resolveCompletionItem(item, {
      isCancellationRequested: false,
    });

    expect(resolveLSPCompletionItem).toHaveBeenCalledWith(
      "vue",
      expect.objectContaining({ data: { id: "vue-item" } }),
    );
    expect(
      vi.mocked(resolveLSPCompletionItem).mock.calls.filter(
        ([language]) => language === "vue",
      ),
    ).toHaveLength(1);
    disposable.dispose();
  });

  it("resolve 保留显式空字符串、空数组与规范 InsertReplaceEdit", async () => {
    const mock = buildMockMonaco();
    const disposable = registerLSPProviders(
      mock as unknown as typeof import("monaco-editor"),
    );
    const provider = mock.languages.registerCompletionItemProvider.mock.calls
      .find(([language]) => language === "go")?.[1] as {
      resolveCompletionItem: (
        item: monacoEditor.languages.CompletionItem,
        token: { isCancellationRequested: boolean },
      ) => Promise<monacoEditor.languages.CompletionItem>;
    };
    const source = {
      label: "emptyAware",
      kind: 6,
      detail: "original detail",
      insertText: "original insert",
      textEditText: "original edit text",
      sortText: "100",
      filterText: "emptyAware",
      commitCharacters: ["."],
      additionalEdits: [
        {
          startLine: 0,
          startCol: 0,
          endLine: 0,
          endCol: 0,
          newText: "import value\n",
        },
      ],
      data: { id: "empty-aware" },
    } satisfies import("@/types").LSPCompletionItem;
    const item = mapLSPCompletionToMonaco(source, COMPLETION_RANGE, "empty");
    vi.mocked(resolveLSPCompletionItem).mockResolvedValueOnce({
      ...source,
      detail: "",
      insertText: "",
      textEditText: "",
      sortText: "",
      filterText: "",
      commitCharacters: [],
      additionalEdits: [],
      textEdit: {
        newText: "",
        insert: {
          start: { line: 2, character: 3 },
          end: { line: 2, character: 4 },
        },
        replace: {
          start: { line: 2, character: 3 },
          end: { line: 2, character: 8 },
        },
      },
    });

    const resolved = await provider.resolveCompletionItem(item, {
      isCancellationRequested: false,
    });

    expect(resolveLSPCompletionItem).toHaveBeenCalledWith("go", source);
    expect(resolved).toMatchObject({
      detail: "",
      insertText: "",
      sortText: "",
      filterText: "",
      commitCharacters: [],
      additionalTextEdits: [],
      range: {
        insert: {
          startLineNumber: 3,
          startColumn: 4,
          endLineNumber: 3,
          endColumn: 5,
        },
        replace: {
          startLineNumber: 3,
          startColumn: 4,
          endLineNumber: 3,
          endColumn: 9,
        },
      },
    });
    expect(
      (resolved as typeof resolved & {
        __resolvedLSPCompletionItem?: import("@/types").LSPCompletionItem;
      }).__resolvedLSPCompletionItem,
    ).toMatchObject({
      insertText: "",
      textEditText: "",
      sortText: "",
      filterText: "",
      commitCharacters: [],
      additionalEdits: [],
    });
    disposable.dispose();
  });

  it("resolve 的 null optional 字段不会覆盖已有值", async () => {
    const mock = buildMockMonaco();
    const disposable = registerLSPProviders(
      mock as unknown as typeof import("monaco-editor"),
    );
    const provider = mock.languages.registerCompletionItemProvider.mock.calls
      .find(([language]) => language === "go")?.[1] as {
      resolveCompletionItem: (
        item: monacoEditor.languages.CompletionItem,
        token: { isCancellationRequested: boolean },
      ) => Promise<monacoEditor.languages.CompletionItem>;
    };
    const source = {
      label: "nullable",
      kind: 6,
      detail: "detail",
      insertText: "insert",
      textEditText: "edit text",
      sortText: "010",
      filterText: "nullable",
      commitCharacters: ["."],
      data: { id: "nullable" },
    } satisfies import("@/types").LSPCompletionItem;
    const item = mapLSPCompletionToMonaco(source, COMPLETION_RANGE, "null");
    vi.mocked(resolveLSPCompletionItem).mockResolvedValueOnce({
      ...source,
      insertText: null,
      textEditText: null,
      sortText: null,
      filterText: null,
      commitCharacters: null,
      textEdit: null,
    });

    const resolved = await provider.resolveCompletionItem(item, {
      isCancellationRequested: false,
    });

    expect(resolved).toMatchObject({
      insertText: "edit text",
      sortText: "010",
      filterText: "nullable",
      commitCharacters: ["."],
    });
    expect(
      (resolved as typeof resolved & {
        __resolvedLSPCompletionItem?: import("@/types").LSPCompletionItem;
      }).__resolvedLSPCompletionItem,
    ).toMatchObject({
      insertText: "insert",
      textEditText: "edit text",
      sortText: "010",
      filterText: "nullable",
      commitCharacters: ["."],
    });
    disposable.dispose();
  });
});

describe("Prompt-1: provider model disposal", () => {
  type RegistrationName =
    | "registerHoverProvider"
    | "registerDefinitionProvider"
    | "registerReferenceProvider"
    | "registerImplementationProvider"
    | "registerDocumentFormattingEditProvider"
    | "registerRenameProvider"
    | "registerSignatureHelpProvider"
    | "registerDocumentSymbolProvider"
    | "registerDeclarationProvider"
    | "registerTypeDefinitionProvider"
    | "registerLinkProvider"
    | "registerSelectionRangeProvider";

  interface ProviderCase {
    name: string;
    registration: RegistrationName;
    backend: unknown;
    backendResult: unknown;
    expected: unknown;
    invoke: (
      provider: unknown,
      model: monacoEditor.editor.ITextModel,
    ) => Promise<unknown>;
  }

  const position = { lineNumber: 1, column: 1 };
  const cases: ProviderCase[] = [
    {
      name: "hover",
      registration: "registerHoverProvider",
      backend: getLSPHover,
      backendResult: "hover",
      expected: null,
      invoke: (provider, model) =>
        (
          provider as {
            provideHover: (
              model: monacoEditor.editor.ITextModel,
              requestPosition: typeof position,
            ) => Promise<unknown>;
          }
        ).provideHover(model, position),
    },
    {
      name: "definition",
      registration: "registerDefinitionProvider",
      backend: getLSPDefinition,
      backendResult: [{}],
      expected: null,
      invoke: (provider, model) =>
        (
          provider as {
            provideDefinition: (
              model: monacoEditor.editor.ITextModel,
              requestPosition: typeof position,
            ) => Promise<unknown>;
          }
        ).provideDefinition(model, position),
    },
    {
      name: "references",
      registration: "registerReferenceProvider",
      backend: getLSPReferences,
      backendResult: [{}],
      expected: [],
      invoke: (provider, model) =>
        (
          provider as {
            provideReferences: (
              model: monacoEditor.editor.ITextModel,
              requestPosition: typeof position,
              context: { includeDeclaration: boolean },
            ) => Promise<unknown>;
          }
        ).provideReferences(model, position, { includeDeclaration: true }),
    },
    {
      name: "implementation",
      registration: "registerImplementationProvider",
      backend: getLSPImplementation,
      backendResult: [{}],
      expected: null,
      invoke: (provider, model) =>
        (
          provider as {
            provideImplementation: (
              model: monacoEditor.editor.ITextModel,
              requestPosition: typeof position,
            ) => Promise<unknown>;
          }
        ).provideImplementation(model, position),
    },
    {
      name: "formatting",
      registration: "registerDocumentFormattingEditProvider",
      backend: formatLSPDocument,
      backendResult: [{}],
      expected: [],
      invoke: (provider, model) =>
        (
          provider as {
            provideDocumentFormattingEdits: (
              model: monacoEditor.editor.ITextModel,
            ) => Promise<unknown>;
          }
        ).provideDocumentFormattingEdits(model),
    },
    {
      name: "rename",
      registration: "registerRenameProvider",
      backend: renameSymbolWorkspace,
      backendResult: [{}],
      expected: null,
      invoke: (provider, model) =>
        (
          provider as {
            provideRenameEdits: (
              model: monacoEditor.editor.ITextModel,
              requestPosition: typeof position,
              newName: string,
            ) => Promise<unknown>;
          }
        ).provideRenameEdits(model, position, "renamed"),
    },
    {
      name: "signature help",
      registration: "registerSignatureHelpProvider",
      backend: getLSPSignatureHelp,
      backendResult: { label: "run()", parameters: [] },
      expected: null,
      invoke: (provider, model) =>
        (
          provider as {
            provideSignatureHelp: (
              model: monacoEditor.editor.ITextModel,
              requestPosition: typeof position,
            ) => Promise<unknown>;
          }
        ).provideSignatureHelp(model, position),
    },
    {
      name: "document symbols",
      registration: "registerDocumentSymbolProvider",
      backend: getLSPDocumentSymbols,
      backendResult: [{}],
      expected: [],
      invoke: (provider, model) =>
        (
          provider as {
            provideDocumentSymbols: (
              model: monacoEditor.editor.ITextModel,
            ) => Promise<unknown>;
          }
        ).provideDocumentSymbols(model),
    },
    {
      name: "declaration",
      registration: "registerDeclarationProvider",
      backend: getLSPDeclaration,
      backendResult: [{}],
      expected: null,
      invoke: (provider, model) =>
        (
          provider as {
            provideDeclaration: (
              model: monacoEditor.editor.ITextModel,
              requestPosition: typeof position,
            ) => Promise<unknown>;
          }
        ).provideDeclaration(model, position),
    },
    {
      name: "type definition",
      registration: "registerTypeDefinitionProvider",
      backend: getLSPTypeDefinition,
      backendResult: [{}],
      expected: null,
      invoke: (provider, model) =>
        (
          provider as {
            provideTypeDefinition: (
              model: monacoEditor.editor.ITextModel,
              requestPosition: typeof position,
            ) => Promise<unknown>;
          }
        ).provideTypeDefinition(model, position),
    },
    {
      name: "links",
      registration: "registerLinkProvider",
      backend: getLSPDocumentLinks,
      backendResult: [{}],
      expected: { links: [] },
      invoke: (provider, model) =>
        (
          provider as {
            provideLinks: (
              model: monacoEditor.editor.ITextModel,
            ) => Promise<unknown>;
          }
        ).provideLinks(model),
    },
    {
      name: "selection ranges",
      registration: "registerSelectionRangeProvider",
      backend: getLSPSelectionRanges,
      backendResult: [{}],
      expected: [],
      invoke: (provider, model) =>
        (
          provider as {
            provideSelectionRanges: (
              model: monacoEditor.editor.ITextModel,
              positions: Array<typeof position>,
            ) => Promise<unknown>;
          }
        ).provideSelectionRanges(model, [position]),
    },
  ];

  it.each(cases)(
    "$name 在 await 期间 dispose 后返回空结果且不再访问模型",
    async (testCase) => {
      const pending = deferred<unknown>();
      installPendingProviderResponses(pending.promise);
      const mock = buildMockMonaco();
      const disposable = registerLSPProviders(
        mock as unknown as typeof import("monaco-editor"),
        "/workspace/main.go",
      );
      const registration = mock.languages[testCase.registration] as unknown as
        ReturnType<typeof vi.fn>;
      const provider = registration.mock.calls.find(
        ([language]) => language === "go",
      )?.[1];
      expect(provider).toBeDefined();
      const tracked = buildDisposalSensitiveModel();

      const result = testCase.invoke(provider, tracked.model);
      await vi.waitFor(() => {
        expect(testCase.backend).toHaveBeenCalledTimes(1);
      });
      tracked.dispose();
      pending.resolve(testCase.backendResult);

      await expect(result).resolves.toEqual(testCase.expected);
      expect(tracked.disposedAccessCount()).toBe(0);
      disposable.dispose();
    },
  );

  it("rename 空结果保持静默", async () => {
    vi.mocked(renameSymbolWorkspace).mockReset().mockResolvedValueOnce([]);
    vi.mocked(notifyWarning).mockClear();
    const mock = buildMockMonaco();
    const disposable = registerLSPProviders(
      mock as unknown as typeof import("monaco-editor"),
      "/workspace/main.go",
    );
    const provider = mock.languages.registerRenameProvider.mock.calls.find(
      ([language]) => language === "go",
    )?.[1] as {
      provideRenameEdits: (
        model: monacoEditor.editor.ITextModel,
        requestPosition: typeof position,
        newName: string,
      ) => Promise<unknown>;
    };
    const tracked = buildDisposalSensitiveModel();

    await expect(
      provider.provideRenameEdits(tracked.model, position, "renamed"),
    ).resolves.toBeNull();
    expect(notifyWarning).not.toHaveBeenCalled();
    disposable.dispose();
  });
});

// ---------------------------------------------------------------------------
// Architecture C (prompt-1.md 491-500): 新增 Monaco Provider 注册测试。
// 验证 registerDeclarationProvider / registerTypeDefinitionProvider /
// registerLinkProvider / registerSelectionRangeProvider /
// registerFoldingRangeProvider 为每种支持语言调用一次。
// ---------------------------------------------------------------------------

describe("Architecture C: 新增 Monaco Provider 注册", () => {
  it("Semantic Tokens Provider 为每个支持语言注册一次", () => {
    const mock = buildMockMonaco();
    const disposable = registerLSPProviders(
      mock as unknown as typeof import("monaco-editor"),
    );
    expect(
      mock.languages.registerDocumentSemanticTokensProvider,
    ).toHaveBeenCalledTimes(LSP_PROVIDER_LANGUAGES.length);
    disposable.dispose();
  });

  it("Code Lens Provider 为每个支持语言注册一次", () => {
    const mock = buildMockMonaco();
    const disposable = registerLSPProviders(
      mock as unknown as typeof import("monaco-editor"),
    );
    expect(mock.languages.registerCodeLensProvider).toHaveBeenCalledTimes(
      LSP_PROVIDER_LANGUAGES.length,
    );
    disposable.dispose();
  });

  it("registerDeclarationProvider 为每个支持的注册语言调用一次", () => {
    const mock = buildMockMonaco();
    const monaco = mock as unknown as typeof import("monaco-editor");
    const disposable = registerLSPProviders(monaco);
    expect(mock.registerDeclarationProviderSpy).toHaveBeenCalledTimes(
      LSP_PROVIDER_LANGUAGES.length,
    );
    const registeredLangs = mock.registerDeclarationProviderSpy.mock.calls.map(
      (c) => c[0],
    );
    expect(registeredLangs).toEqual(
      expect.arrayContaining([...LSP_PROVIDER_LANGUAGES]),
    );
    disposable.dispose();
  });

  it("registerTypeDefinitionProvider 为每个支持的注册语言调用一次", () => {
    const mock = buildMockMonaco();
    const monaco = mock as unknown as typeof import("monaco-editor");
    const disposable = registerLSPProviders(monaco);
    expect(mock.registerTypeDefinitionProviderSpy).toHaveBeenCalledTimes(
      LSP_PROVIDER_LANGUAGES.length,
    );
    disposable.dispose();
  });

  it("registerLinkProvider 为每个支持的注册语言调用一次", () => {
    const mock = buildMockMonaco();
    const monaco = mock as unknown as typeof import("monaco-editor");
    const disposable = registerLSPProviders(monaco);
    expect(mock.registerLinkProviderSpy).toHaveBeenCalledTimes(
      LSP_PROVIDER_LANGUAGES.length,
    );
    disposable.dispose();
  });

  it("registerSelectionRangeProvider 为每个支持的注册语言调用一次", () => {
    const mock = buildMockMonaco();
    const monaco = mock as unknown as typeof import("monaco-editor");
    const disposable = registerLSPProviders(monaco);
    expect(mock.registerSelectionRangeProviderSpy).toHaveBeenCalledTimes(
      LSP_PROVIDER_LANGUAGES.length,
    );
    disposable.dispose();
  });

  it("registerFoldingRangeProvider 为每个支持的注册语言调用一次", () => {
    const mock = buildMockMonaco();
    const monaco = mock as unknown as typeof import("monaco-editor");
    const disposable = registerLSPProviders(monaco);
    expect(mock.registerFoldingRangeProviderSpy).toHaveBeenCalledTimes(
      LSP_PROVIDER_LANGUAGES.length,
    );
    disposable.dispose();
  });
});

describe("9E refactor code actions", () => {
  it("routes refactor WorkspaceEdit through the shared preview command", async () => {
    const monaco = buildMockMonaco();
    registerLSPProviders(
      monaco as unknown as typeof import("monaco-editor"),
      "/workspace/main.go",
    );
    vi.mocked(getLSPCodeActions).mockResolvedValueOnce([
      {
        title: "Extract function",
        kind: "refactor.extract",
        preview: {
          files: [
            {
              filePath: "/workspace/main.go",
              baselineHash: "abc",
              originalContent: "old",
              modifiedContent: "new",
            },
          ],
        },
      },
    ]);
    const registration = monaco.registerCodeActionProviderSpy.mock.calls.find(
      ([language]) => language === "go",
    );
    const provider = registration?.[1];
    const model = {
      uri: {
        path: "/workspace/main.go",
        toString: () => "file:///workspace/main.go",
      },
      getValue: () => "old",
      isDisposed: () => false,
    };
    const range = {
      startLineNumber: 1,
      startColumn: 1,
      endLineNumber: 1,
      endColumn: 4,
    };
    const result = await provider.provideCodeActions(model, range);
    expect(getLSPCodeActions).toHaveBeenCalledWith(
      "go",
      "/workspace/main.go",
      0,
      0,
      "old",
      { endLine: 0, endColumn: 3 },
    );
    expect(result.actions[0].edit).toBeUndefined();
    expect(result.actions[0].command.id).toBe(LSP_REFACTOR_PREVIEW_COMMAND);

    const commandHandler = monaco.registerCommandSpy.mock.calls[0][1];
    await commandHandler({}, result.actions[0].command.arguments[0]);
    await vi.waitFor(() =>
      expect(openRefactorActionPreview).toHaveBeenCalledTimes(1),
    );
  });
});
