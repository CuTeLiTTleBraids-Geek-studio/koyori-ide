import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type * as monacoEditor from "monaco-editor";

const mocks = vi.hoisted(() => ({
  getPullDiagnostics: vi.fn(),
  getRefreshVersion: vi.fn(),
  eventsOn: vi.fn(),
  cancelRefreshEvent: vi.fn(),
  refreshEvent: null as ((event: unknown) => void) | null,
  appState: { currentFilePath: "" },
  editorState: {
    openFiles: [] as Array<{ path: string; content: string }>,
    activeFilePath: null as string | null,
  },
}));

vi.mock("@wailsio/runtime", () => ({
  Events: { On: mocks.eventsOn },
}));
vi.mock("@/api/services", () => ({
  lspService: {
    detectServers: vi.fn(),
    startServer: vi.fn(),
    stopServer: vi.fn(),
  },
}));
vi.mock("@/stores/app", () => ({
  appState: mocks.appState,
}));
vi.mock("@/stores/editor", () => ({
  editorState: mocks.editorState,
}));
vi.mock("@/stores/output", () => ({
  pushOutput: vi.fn(),
}));
vi.mock("../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/lspservice.js", () => ({
  GetPullDiagnostics: mocks.getPullDiagnostics,
  GetDiagnosticsRefreshVersion: mocks.getRefreshVersion,
}));

import {
  __resetLSPStoreForTesting,
  cleanupPullDiagnostics,
  lspState,
  registerPullDiagnostics,
} from "./lsp";

interface ModelState {
  content: string;
  disposed: boolean;
  version: number;
  throwOnRead: boolean;
}

function buildHarness(options: { virtual?: boolean } = {}) {
  const state: ModelState = {
    content: "package main\n",
    disposed: false,
    version: 1,
    throwOnRead: false,
  };
  let contentListener: (() => void) | null = null;
  let createListener:
    | ((model: monacoEditor.editor.ITextModel) => void)
    | null = null;
  let disposeListener:
    | ((model: monacoEditor.editor.ITextModel) => void)
    | null = null;
  const contentDisposable = { dispose: vi.fn() };
  const lifecycleDisposables = [
    { dispose: vi.fn() },
    { dispose: vi.fn() },
    { dispose: vi.fn() },
  ];
  const model = {
    uri: options.virtual
      ? {
          fsPath: "",
          path: "/model/1",
          toString: () => "inmemory://model/1",
        }
      : {
          fsPath: "/workspace/main.go",
          path: "/workspace/main.go",
          toString: () => "file:///workspace/main.go",
        },
    getValue: vi.fn(() => {
      if (state.throwOnRead) throw new Error("model unavailable");
      return state.content;
    }),
    getVersionId: vi.fn(() => state.version),
    getLanguageId: vi.fn(() => "go"),
    isDisposed: vi.fn(() => state.disposed),
    onDidChangeContent: vi.fn((listener: () => void) => {
      contentListener = listener;
      return contentDisposable;
    }),
  } as unknown as monacoEditor.editor.ITextModel;
  const setModelMarkers = vi.fn();
  const monaco = {
    MarkerSeverity: {
      Error: 8,
      Warning: 4,
      Info: 2,
      Hint: 1,
    },
    editor: {
      getModels: vi.fn(() => [model]),
      onDidCreateModel: vi.fn(
        (listener: (created: monacoEditor.editor.ITextModel) => void) => {
          createListener = listener;
          return lifecycleDisposables[0];
        },
      ),
      onWillDisposeModel: vi.fn(
        (listener: (disposed: monacoEditor.editor.ITextModel) => void) => {
          disposeListener = listener;
          return lifecycleDisposables[1];
        },
      ),
      onDidChangeModelLanguage: vi.fn(() => lifecycleDisposables[2]),
      setModelMarkers,
    },
  } as unknown as typeof import("monaco-editor");

  return {
    state,
    model,
    monaco,
    setModelMarkers,
    contentDisposable,
    lifecycleDisposables,
    contentListener: () => contentListener,
    createListener: () => createListener,
    disposeListener: () => disposeListener,
  };
}

async function advance(ms = 0): Promise<void> {
  await vi.advanceTimersByTimeAsync(ms);
  for (let index = 0; index < 8; index += 1) {
    await Promise.resolve();
  }
  await vi.advanceTimersByTimeAsync(0);
  for (let index = 0; index < 8; index += 1) {
    await Promise.resolve();
  }
}

beforeEach(() => {
  vi.useFakeTimers();
  __resetLSPStoreForTesting();
  vi.clearAllMocks();
  mocks.refreshEvent = null;
  mocks.eventsOn.mockImplementation(
    (_name: string, listener: (event: unknown) => void) => {
      mocks.refreshEvent = listener;
      return mocks.cancelRefreshEvent;
    },
  );
  mocks.getRefreshVersion.mockResolvedValue(0);
  mocks.appState.currentFilePath = "";
  mocks.editorState.openFiles = [];
  mocks.editorState.activeFilePath = null;
  lspState.statuses.go = {
    language: "go",
    available: true,
    running: true,
    serverPath: "/usr/bin/gopls",
    version: "test",
  };
});

afterEach(() => {
  cleanupPullDiagnostics();
  vi.useRealTimers();
});

describe("pull diagnostics registration", () => {
  it("resolves a virtual model only when its content uniquely matches an open file", async () => {
    const harness = buildHarness({ virtual: true });
    mocks.editorState.openFiles = [
      { path: "/workspace/main.go", content: harness.state.content },
    ];
    mocks.editorState.activeFilePath = "/workspace/main.go";
    mocks.getPullDiagnostics.mockResolvedValue([]);

    registerPullDiagnostics(harness.monaco, {
      refreshPollIntervalMs: 0,
    });

    await advance();
    expect(mocks.getPullDiagnostics).toHaveBeenCalledOnce();
    expect(mocks.getPullDiagnostics).toHaveBeenCalledWith(
      expect.objectContaining({ filePath: "/workspace/main.go" }),
    );
  });

  it("skips an unmatched virtual model instead of falling back to the active file", async () => {
    const harness = buildHarness({ virtual: true });
    mocks.appState.currentFilePath = "/workspace/active.go";
    mocks.editorState.openFiles = [
      { path: "/workspace/active.go", content: "package active\n" },
    ];
    mocks.editorState.activeFilePath = "/workspace/active.go";
    mocks.getPullDiagnostics.mockResolvedValue([]);

    registerPullDiagnostics(harness.monaco, {
      refreshPollIntervalMs: 0,
    });

    await advance();
    expect(mocks.getPullDiagnostics).not.toHaveBeenCalled();
    expect(harness.setModelMarkers).toHaveBeenLastCalledWith(
      harness.model,
      "koyori-ide-lsp-pull",
      [],
    );
  });

  it("skips a virtual model when duplicate open-file contents make the path ambiguous", async () => {
    const harness = buildHarness({ virtual: true });
    mocks.appState.currentFilePath = "/workspace/active.go";
    mocks.editorState.openFiles = [
      { path: "/workspace/active.go", content: harness.state.content },
      { path: "/workspace/copy.go", content: harness.state.content },
    ];
    mocks.editorState.activeFilePath = "/workspace/active.go";
    mocks.getPullDiagnostics.mockResolvedValue([]);

    registerPullDiagnostics(harness.monaco, {
      refreshPollIntervalMs: 0,
    });

    await advance();
    expect(mocks.getPullDiagnostics).not.toHaveBeenCalled();
    expect(harness.setModelMarkers).toHaveBeenLastCalledWith(
      harness.model,
      "koyori-ide-lsp-pull",
      [],
    );
  });

  it("refreshes on attach, server events and debounced content changes, then cleans up", async () => {
    const harness = buildHarness();
    mocks.getPullDiagnostics.mockResolvedValue([
      {
        line: 1,
        column: 2,
        endLine: 1,
        endColumn: 5,
        severity: 1,
        message: "broken",
        source: "gopls",
      },
    ]);
    const registration = registerPullDiagnostics(harness.monaco, {
      changeDebounceMs: 10,
      refreshPollIntervalMs: 0,
    });

    await advance();
    expect(mocks.getPullDiagnostics).toHaveBeenCalledTimes(1);
    expect(harness.setModelMarkers).toHaveBeenLastCalledWith(
      harness.model,
      "koyori-ide-lsp-pull",
      [
        expect.objectContaining({
          startLineNumber: 2,
          startColumn: 3,
          endLineNumber: 2,
          endColumn: 6,
          severity: 8,
          message: "broken",
          modelVersionId: 1,
        }),
      ],
    );

    mocks.refreshEvent?.({ data: { language: "go" } });
    await advance();
    expect(mocks.getPullDiagnostics).toHaveBeenCalledTimes(2);

    harness.state.content = "package main\nfunc main() {}\n";
    harness.state.version = 2;
    harness.contentListener()?.();
    await advance(9);
    expect(mocks.getPullDiagnostics).toHaveBeenCalledTimes(2);
    await advance(1);
    expect(mocks.getPullDiagnostics).toHaveBeenCalledTimes(3);

    registration.dispose();
    expect(mocks.cancelRefreshEvent).toHaveBeenCalledOnce();
    expect(harness.contentDisposable.dispose).toHaveBeenCalledOnce();
    for (const disposable of harness.lifecycleDisposables) {
      expect(disposable.dispose).toHaveBeenCalledOnce();
    }
    expect(harness.setModelMarkers).toHaveBeenLastCalledWith(
      harness.model,
      "koyori-ide-lsp-pull",
      [],
    );

    mocks.refreshEvent?.({ data: { language: "go" } });
    harness.contentListener()?.();
    await advance(20);
    expect(mocks.getPullDiagnostics).toHaveBeenCalledTimes(3);
  });

  it("drops a stale pull response after a newer model generation wins", async () => {
    const harness = buildHarness();
    let resolveFirst!: (value: unknown[]) => void;
    const first = new Promise<unknown[]>((resolve) => {
      resolveFirst = resolve;
    });
    mocks.getPullDiagnostics
      .mockReturnValueOnce(first)
      .mockResolvedValueOnce([
        {
          line: 2,
          column: 0,
          endLine: 2,
          endColumn: 1,
          severity: 2,
          message: "current",
          source: "gopls",
        },
      ]);
    registerPullDiagnostics(harness.monaco, {
      changeDebounceMs: 0,
      refreshPollIntervalMs: 0,
    });

    await advance();
    harness.state.content = "package main\nvar value = 1\n";
    harness.state.version = 2;
    harness.contentListener()?.();
    await advance();
    expect(harness.setModelMarkers).toHaveBeenLastCalledWith(
      harness.model,
      "koyori-ide-lsp-pull",
      [expect.objectContaining({ message: "current", modelVersionId: 2 })],
    );

    resolveFirst([
      {
        line: 0,
        column: 0,
        endLine: 0,
        endColumn: 1,
        severity: 1,
        message: "stale",
        source: "gopls",
      },
    ]);
    await Promise.resolve();
    await Promise.resolve();
    expect(harness.setModelMarkers).toHaveBeenLastCalledWith(
      harness.model,
      "koyori-ide-lsp-pull",
      [expect.objectContaining({ message: "current", modelVersionId: 2 })],
    );
  });

  it("polls refresh generations and schedules a new pull only when the version changes", async () => {
    vi.useRealTimers();
    const harness = buildHarness();
    let refreshVersion = 0;
    mocks.getPullDiagnostics.mockResolvedValue([]);
    mocks.getRefreshVersion.mockImplementation(async () => refreshVersion);
    registerPullDiagnostics(harness.monaco, {
      changeDebounceMs: 0,
      refreshPollIntervalMs: 5,
    });

    await vi.waitFor(() =>
      expect(mocks.getPullDiagnostics).toHaveBeenCalledTimes(1),
    );
    refreshVersion = 1;
    await vi.waitFor(() =>
      expect(mocks.getPullDiagnostics).toHaveBeenCalledTimes(2),
    );
    await new Promise((resolve) => setTimeout(resolve, 15));
    expect(mocks.getPullDiagnostics).toHaveBeenCalledTimes(2);

    refreshVersion = 2;
    await vi.waitFor(() =>
      expect(mocks.getPullDiagnostics).toHaveBeenCalledTimes(3),
    );
    expect(mocks.getRefreshVersion).toHaveBeenCalledWith("go");
  });

  it("swallows model access failures from timer callbacks", async () => {
    const harness = buildHarness();
    const debug = vi.spyOn(console, "debug").mockImplementation(() => undefined);
    harness.state.throwOnRead = true;
    registerPullDiagnostics(harness.monaco, {
      refreshPollIntervalMs: 0,
    });

    await expect(advance()).resolves.toBeUndefined();
    expect(mocks.getPullDiagnostics).not.toHaveBeenCalled();
    expect(debug).toHaveBeenCalledWith(
      "[LSP]",
      "refreshPullDiagnostics",
      expect.any(Error),
    );
    debug.mockRestore();
  });
});
