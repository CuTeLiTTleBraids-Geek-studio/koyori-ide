import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

interface Deferred<T> {
  promise: Promise<T>;
  resolve(value: T): void;
}

interface TestCompletionProvider {
  provideCompletionItems(
    model: { getWordUntilPosition(): { startColumn: number; endColumn: number } },
    position: { lineNumber: number },
  ): { suggestions: Array<{ label: string }> };
}

function deferred<T>(): Deferred<T> {
  let resolvePromise: (value: T) => void = () => undefined;
  const promise = new Promise<T>((resolve) => {
    resolvePromise = resolve;
  });
  return { promise, resolve: resolvePromise };
}

function snippetBytes(label: string): Uint8Array {
  return new TextEncoder().encode(JSON.stringify({
    [label]: {
      prefix: label,
      body: [`${label} body`],
      description: `${label} docs`,
    },
  }));
}

const mocks = vi.hoisted(() => ({
  registry: {
    value: {} as Record<string, Array<{ extensionId: string; language: string; path: string }>>,
  },
  grammarRegistry: {
    value: {} as Record<string, Array<{ extensionId: string; language: string; scopeName: string; path: string }>>,
  },
  readExtensionFile: vi.fn(),
  registerCompletionItemProvider: vi.fn(),
  registerLanguage: vi.fn(),
  getLanguages: vi.fn(() => [] as Array<{ id: string }>),
  disposables: [] as Array<{ dispose: ReturnType<typeof vi.fn> }>,
}));

vi.mock("monaco-editor", () => ({
  languages: {
    getLanguages: mocks.getLanguages,
    register: mocks.registerLanguage,
    registerCompletionItemProvider: mocks.registerCompletionItemProvider,
    CompletionItemKind: { Snippet: 27 },
    CompletionItemInsertTextRule: { InsertAsSnippet: 4 },
  },
}));

vi.mock("@/api/services", () => ({
  marketplaceService: {
    readExtensionFile: mocks.readExtensionFile,
  },
}));

vi.mock("@/lib/vscodeExtensions", () => ({
  listAllVscodeExtensionGrammars: vi.fn(() => mocks.grammarRegistry.value),
  listAllVscodeExtensionSnippets: vi.fn(() => mocks.registry.value),
}));

import {
  clearMonacoExtensionContributes,
  getLanguageForScope,
  syncExtensionGrammarsToMonaco,
  syncExtensionSnippetsToMonaco,
} from "./monacoExtensionContributes";

function setSnippet(path: string): void {
  mocks.registry.value = {
    typescript: [{ extensionId: "acme.demo", language: "typescript", path }],
  };
}

function latestProviderLabels(): string[] {
  const call = mocks.registerCompletionItemProvider.mock.calls.at(-1);
  if (!call) return [];
  const provider = call[1] as TestCompletionProvider;
  return provider.provideCompletionItems(
    { getWordUntilPosition: () => ({ startColumn: 1, endColumn: 1 }) },
    { lineNumber: 1 },
  ).suggestions.map((item) => item.label);
}

describe("Monaco extension snippet synchronization", () => {
  beforeEach(() => {
    clearMonacoExtensionContributes();
    vi.clearAllMocks();
    mocks.registry.value = {};
    mocks.grammarRegistry.value = {};
    mocks.disposables.length = 0;
    mocks.registerCompletionItemProvider.mockImplementation(() => {
      const disposable = { dispose: vi.fn() };
      mocks.disposables.push(disposable);
      return disposable;
    });
  });

  afterEach(() => {
    clearMonacoExtensionContributes();
  });

  it("invalidates an in-flight sync when HMR cleanup runs", async () => {
    const pending = deferred<Uint8Array>();
    setSnippet("snippets.json");
    mocks.readExtensionFile.mockReturnValueOnce(pending.promise);

    const staleSync = syncExtensionSnippetsToMonaco();
    clearMonacoExtensionContributes();
    pending.resolve(snippetBytes("stale"));
    await staleSync;

    expect(mocks.registerCompletionItemProvider).not.toHaveBeenCalled();

    mocks.readExtensionFile.mockResolvedValueOnce(snippetBytes("fresh"));
    await syncExtensionSnippetsToMonaco();

    expect(mocks.readExtensionFile).toHaveBeenCalledTimes(2);
    expect(latestProviderLabels()).toEqual(["fresh"]);
  });

  it("keeps the newer sync when an older read resolves later", async () => {
    const oldRead = deferred<Uint8Array>();
    const newRead = deferred<Uint8Array>();
    mocks.readExtensionFile.mockImplementation(
      (_publisher: string, _name: string, path: string) =>
        path === "old.json" ? oldRead.promise : newRead.promise,
    );

    setSnippet("old.json");
    const oldSync = syncExtensionSnippetsToMonaco();
    setSnippet("new.json");
    const newSync = syncExtensionSnippetsToMonaco();

    newRead.resolve(snippetBytes("new"));
    await newSync;
    oldRead.resolve(snippetBytes("old"));
    await oldSync;

    expect(mocks.registerCompletionItemProvider).toHaveBeenCalledTimes(1);
    expect(latestProviderLabels()).toEqual(["new"]);
    expect(mocks.disposables[0].dispose).not.toHaveBeenCalled();
  });

  it("retries a snippet file after a transient read failure", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    setSnippet("retry.json");
    mocks.readExtensionFile
      .mockRejectedValueOnce(new Error("temporary"))
      .mockResolvedValueOnce(snippetBytes("retry"));

    await syncExtensionSnippetsToMonaco();
    await syncExtensionSnippetsToMonaco();

    expect(mocks.readExtensionFile).toHaveBeenCalledTimes(2);
    expect(mocks.registerCompletionItemProvider).toHaveBeenCalledTimes(1);
    expect(latestProviderLabels()).toEqual(["retry"]);
    warn.mockRestore();
  });

  it("removes a provider when its extension contribution disappears", async () => {
    setSnippet("remove.json");
    mocks.readExtensionFile.mockResolvedValueOnce(snippetBytes("remove"));
    await syncExtensionSnippetsToMonaco();
    const disposable = mocks.disposables[0];

    mocks.registry.value = {};
    await syncExtensionSnippetsToMonaco();

    expect(disposable.dispose).toHaveBeenCalledOnce();
    expect(mocks.registerCompletionItemProvider).toHaveBeenCalledTimes(1);
  });

  it("can register from cache after Monaco rejects the first provider", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    setSnippet("provider.json");
    mocks.readExtensionFile.mockResolvedValueOnce(snippetBytes("provider"));
    mocks.registerCompletionItemProvider
      .mockImplementationOnce(() => {
        throw new Error("Monaco unavailable");
      })
      .mockImplementationOnce(() => {
        const disposable = { dispose: vi.fn() };
        mocks.disposables.push(disposable);
        return disposable;
      });

    await syncExtensionSnippetsToMonaco();
    await syncExtensionSnippetsToMonaco();

    expect(mocks.readExtensionFile).toHaveBeenCalledTimes(1);
    expect(mocks.registerCompletionItemProvider).toHaveBeenCalledTimes(2);
    expect(latestProviderLabels()).toEqual(["provider"]);
    warn.mockRestore();
  });

  it("removes grammar scope mappings that disappeared from the registry", () => {
    mocks.grammarRegistry.value = {
      typescript: [{
        extensionId: "acme.demo",
        language: "typescript",
        scopeName: "source.acme",
        path: "grammar.json",
      }],
    };
    syncExtensionGrammarsToMonaco();
    expect(getLanguageForScope("source.acme")).toBe("typescript");

    mocks.grammarRegistry.value = {};
    syncExtensionGrammarsToMonaco();

    expect(getLanguageForScope("source.acme")).toBeUndefined();
  });
});
