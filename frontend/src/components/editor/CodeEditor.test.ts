import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";

// H-11: 验证多个 CodeEditor 实例的装饰状态相互隔离。
// 通过 vi.hoisted 暴露共享 mock 状态，便于在 vi.mock 工厂中引用。
const {
  mountedEditors,
  fakeMonaco,
  coverageHolder,
  debugHolder,
  appStateHolder,
  disposableTracker,
  editorCreate,
  registerLSPProviders,
  lspProviderDisposables,
  registerEmmetProviders,
  emmetDispose,
  inlineProviderDispose,
  inlineProviders,
  requestCompletion,
  cancelInlineCompletion,
  gitGetBlame,
  gitGetBlameForRange,
  setPanelTab,
  setSidebarVisible,
  setCallHierarchyQuery,
  layoutState,
} = vi.hoisted(() => {
  const mountedEditors: any[] = [];

  // M-11/M-12: 跟踪所有创建与释放的 IDisposable，用于断言卸载时全部释放。
  const disposableTracker: { created: number; disposed: number } = {
    created: 0,
    disposed: 0,
  };
  function makeDisposable() {
    disposableTracker.created += 1;
    return {
      dispose: () => {
        disposableTracker.disposed += 1;
      },
    };
  }

  // 覆盖 CodeEditor 挂载时用到的全部 monaco 运行时 API。
  const editorCreate = vi.fn((_container: unknown, _options: unknown) =>
    makeFakeEditor(),
  );
  const lspProviderDisposables: Array<{ dispose: ReturnType<typeof vi.fn> }> =
    [];
  const registerLSPProviders = vi.fn(() => {
    const disposable = { dispose: vi.fn() };
    lspProviderDisposables.push(disposable);
    return disposable;
  });
  const emmetDispose = vi.fn();
  const registerEmmetProviders = vi.fn(() => ({ dispose: emmetDispose }));
  const inlineProviderDispose = vi.fn();
  const inlineProviders: Array<{
    provideInlineCompletions: (
      model: unknown,
      position: { lineNumber: number; column: number },
    ) => Promise<{ items: unknown[] }>;
  }> = [];
  const requestCompletion = vi.fn().mockResolvedValue("suggestion");
  const cancelInlineCompletion = vi.fn();
  const gitGetBlame = vi.fn().mockResolvedValue([]);
  const gitGetBlameForRange = vi.fn().mockResolvedValue([]);
  const setPanelTab = vi.fn();
  const setSidebarVisible = vi.fn();
  const setCallHierarchyQuery = vi.fn();
  const layoutState = { tree: { activeLeafId: "group-a" } };

  const fakeMonaco = {
    Range: class {
      a: number;
      b: number;
      c: number;
      d: number;
      constructor(a: number, b: number, c: number, d: number) {
        this.a = a;
        this.b = b;
        this.c = c;
        this.d = d;
      }
    },
    editor: {
      create: editorCreate,
      OverviewRulerLane: { Left: 1 },
      MouseTargetType: { GUTTER_GLYPH_MARGIN: 2 },
    },
    KeyMod: {
      Alt: 512,
      CtrlCmd: 2048,
      Shift: 1024,
      chord: (first: number, second: number) =>
        ((first & 0xffff) << 16) | (second & 0xffff),
    },
    KeyCode: {
      Tab: 2,
      KeyD: 24,
      KeyI: 29,
      KeyK: 33,
      KeyL: 34,
      KeyU: 47,
      UpArrow: 16,
      DownArrow: 18,
      Backslash: 88,
    },
    languages: {
      registerInlineCompletionsProvider: (
        _selector: unknown,
        provider: (typeof inlineProviders)[number],
      ) => {
        inlineProviders.push(provider);
        return { dispose: inlineProviderDispose };
      },
      registerCompletionItemProvider: () => ({ dispose: () => undefined }),
    },
  };

  // 每个实例拥有独立的假 editor，记录 deltaDecorations 的 (old, newCount) 调用。
  function makeFakeEditor() {
    const calls: Array<{
      old: string[];
      newCount: number;
      lineClasses: Array<string | undefined>;
      gutterClasses: Array<string | undefined>;
      overviewColors: Array<string | undefined>;
      afterContents: Array<string | undefined>;
      hoverMessages: Array<string | undefined>;
    }> = [];
    const optionUpdates: unknown[] = [];
    const actions: Array<{
      id?: string;
      keybindings?: number[];
      run?: (...args: any[]) => unknown;
    }> = [];
    // M-11: 收集 onDidChangeModelContent 注册的回调，测试可手动触发。
    const contentChangeCallbacks: Array<() => void> = [];
    const cursorChangeCallbacks: Array<
      (event: { position: { lineNumber: number; column: number } }) => void
    > = [];
    const model = {
      getLanguageId: () => "typescript",
      getValue: () => "const answer = 42;",
      getValueInRange: () => "const answer = ",
      getLineCount: () => 1,
      getLineMaxColumn: () => 19,
      isDisposed: () => false,
    };
    return {
      calls,
      optionUpdates,
      actions,
      contentChangeCallbacks,
      cursorChangeCallbacks,
      updateOptions: (options: unknown) => {
        optionUpdates.push(options);
      },
      onDidChangeCursorPosition: (
        cb: (event: {
          position: { lineNumber: number; column: number };
        }) => void,
      ) => {
        cursorChangeCallbacks.push(cb);
        return makeDisposable();
      },
      onMouseDown: () => makeDisposable(),
      onDidFocusEditorWidget: () => makeDisposable(),
      trigger: vi.fn(),
      // M-12: addAction 返回可跟踪的 IDisposable。
      addAction: (action: { keybindings?: number[] }) => {
        actions.push(action);
        return makeDisposable();
      },
      // M-11: onDidChangeModelContent 记录回调并返回可跟踪的 IDisposable。
      onDidChangeModelContent: (cb: () => void) => {
        contentChangeCallbacks.push(cb);
        return makeDisposable();
      },
      deltaDecorations: (old: string[], newDecs: any[]) => {
        calls.push({
          old: [...old],
          newCount: newDecs.length,
          lineClasses: newDecs.map((dec) => dec.options?.className),
          gutterClasses: newDecs.map(
            (dec) => dec.options?.linesDecorationsClassName,
          ),
          overviewColors: newDecs.map(
            (dec) => dec.options?.overviewRuler?.color,
          ),
          afterContents: newDecs.map((dec) => dec.options?.after?.content),
          hoverMessages: newDecs.map((dec) => dec.options?.hoverMessage?.value),
        });
        return newDecs.map((_, i) => `d-${calls.length}-${i}`);
      },
      getValue: () => "",
      getPosition: () => ({ lineNumber: 1, column: 1 }),
      getSelection: () => ({
        startLineNumber: 3,
        startColumn: 4,
        isEmpty: () => true,
      }),
      getModel: () => model,
      dispose: () => undefined,
      focus: vi.fn(),
      setPosition: vi.fn(),
      revealLineInCenter: vi.fn(),
    };
  }

  const coverageHolder: { state: any } = { state: null };
  const debugHolder: { state: any } = { state: null };
  const appStateHolder: { state: any } = { state: null };

  return {
    mountedEditors,
    fakeMonaco,
    makeFakeEditor,
    coverageHolder,
    debugHolder,
    appStateHolder,
    disposableTracker,
    editorCreate,
    registerLSPProviders,
    lspProviderDisposables,
    registerEmmetProviders,
    emmetDispose,
    inlineProviderDispose,
    inlineProviders,
    requestCompletion,
    cancelInlineCompletion,
    gitGetBlame,
    gitGetBlameForRange,
    setPanelTab,
    setSidebarVisible,
    setCallHierarchyQuery,
    layoutState,
  };
});

// 用 Options API 定义 mock 组件，避免在 vi.mock 工厂中依赖 vue 运行时导入。
vi.mock("@guolao/vue-monaco-editor", () => ({
  VueMonacoEditor: {
    name: "VueMonacoEditor",
    props: ["value", "language", "theme", "options"],
    emits: ["mount", "update:content"],
    mounted(this: any) {
      const editor = fakeMonaco.editor.create(null, this.options);
      this.fakeEditor = editor;
      mountedEditors.push(editor);
      this.$emit("mount", editor, fakeMonaco);
    },
    watch: {
      options: {
        deep: true,
        handler(this: any, options: unknown) {
          this.fakeEditor?.updateOptions(options);
        },
      },
    },
    render() {
      return null;
    },
  },
}));

vi.mock("@/stores/app", async () => {
  const { reactive } = await import("vue");
  const state = reactive({
    theme: "dark",
    accentTheme: "blue",
    fontSize: 14,
    fontFamily: "monospace",
    tabSize: 2,
    gitBlameEnabled: false,
    currentProject: null,
    workspaceFolders: [] as string[],
    currentFilePath: null,
    editorJumpSeq: 0,
    editorJumpTargetPath: null,
    editorJumpTargetGroupId: null,
    editorJumpTargetSeq: 0,
    cursorLine: 1,
    cursorColumn: 1,
    inlineCompletionEnabled: false,
    emmetEnabled: true,
    emmetIncludeLanguages: { vue: "html" },
    insertSpaces: true,
    wordWrap: false,
    lineNumbers: true,
    minimap: false,
    minimapEnabled: false,
    stickyScrollEnabled: true,
    inlayHintsEnabled: true,
    organizeImportsOnSave: true,
    cursorBlinking: "smooth",
    cursorStyle: "line",
    bracketColorization: true,
  });
  appStateHolder.state = state;
  return {
    appState: state,
    cloneEditorGroup: vi.fn(),
    setPanelTab,
    setSidebarVisible,
  };
});

vi.mock("@/stores/layout", () => ({
  layoutState,
  splitLeaf: vi.fn(),
}));

vi.mock("@/stores/coverage", async () => {
  const { reactive } = await import("vue");
  const state = reactive({ hits: [] as unknown[] });
  coverageHolder.state = state;
  return {
    coverageState: state,
    // 仅 /a/file.ts 命中 2 条 coverage，其它路径无命中。
    coverageHitsForFile: (path: string) =>
      path === "/a/file.ts"
        ? [
            { line: 1, covered: true },
            { line: 2, covered: false },
          ]
        : path === "/partial/file.ts"
          ? [
              {
                line: 7,
                covered: true,
                status: "partial",
                coveredCount: 1,
                totalCount: 2,
              },
            ]
          : [],
  };
});

vi.mock("@/stores/debug", async () => {
  const { reactive } = await import("vue");
  const state = reactive({
    breakpoints: [],
    stopped: false,
    stack: [],
    running: false,
  });
  debugHolder.state = state;
  return {
    debugState: state,
    breakpointsForFile: () => [],
    toggleBreakpoint: vi.fn().mockResolvedValue(undefined),
    launchDebugPackage: vi.fn(),
    launchCurrentFile: vi.fn(),
    debugContinue: vi.fn(),
    debugTestAtCursor: vi.fn(),
  };
});

// H-11 安全网：即使真实 debug.ts 被加载，也不会因级联导入 output.ts 而崩溃。
vi.mock("@/stores/output", () => ({
  pushOutput: vi.fn(),
}));

// H-11 安全网：scheduleLiveDiagnostics 动态导入 lsp，mock 防止真实模块加载。
vi.mock("@/stores/lsp", () => ({
  refreshDiagnosticsToProblems: vi.fn(),
  setCallHierarchyQuery,
}));

vi.mock("@/api/services", () => ({
  windowService: { sendSelectionToAI: vi.fn().mockResolvedValue(undefined) },
  gitService: { getBlame: gitGetBlame },
}));

vi.mock("../../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/gitservice.js", () => ({
  GetBlameForRange: gitGetBlameForRange,
}));

vi.mock("@/lib/lspCompletion", () => ({
  registerLSPProviders,
}));

vi.mock("@/lib/emmet", () => ({
  registerEmmetProviders,
}));

vi.mock("@/lib/monaco-themes", () => ({
  getMonacoThemeNameForMode: () => "vs",
}));

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (k: string) => k }),
}));

vi.mock("@/lib/language", () => ({
  detectLanguage: () => "typescript",
}));

vi.mock("@/stores/ai", () => ({
  runAIAction: vi.fn(),
}));

vi.mock("@/stores/inlineCompletion", () => ({
  requestCompletion,
  cancelInlineCompletion,
}));

vi.mock("@/stores/toolchain", () => ({
  runToolchainCommand: vi.fn(),
  runToolchainCommandQuiet: vi.fn(),
}));

vi.mock("@/lib/notifications", () => ({
  notifyWarning: vi.fn(),
  notifySuccess: vi.fn(),
}));

import CodeEditor from "./CodeEditor.vue";
import { watch } from "vue";

function mountEditor(path: string, groupId?: string) {
  return mount(CodeEditor, {
    props: { path, content: "", groupId },
  });
}

const expectedStableEditorOptions = {
  stickyScroll: {
    enabled: true,
    maxLineCount: 5,
    defaultModel: "outlineModel",
  },
  multiCursorModifier: "ctrlCmd",
  columnSelection: true,
  cursorSmoothCaretAnimation: "on",
  smoothScrolling: true,
  bracketPairColorization: {
    enabled: true,
    independentColorPoolPerBracketType: true,
  },
};

describe("7D: Monaco sticky scroll and multi-cursor options", () => {
  beforeEach(() => {
    mountedEditors.length = 0;
    editorCreate.mockClear();
    appStateHolder.state.fontSize = 14;
  });

  afterEach(() => {
    appStateHolder.state.fontSize = 14;
    appStateHolder.state.minimapEnabled = false;
    appStateHolder.state.stickyScrollEnabled = true;
    appStateHolder.state.inlayHintsEnabled = true;
  });

  it("passes stable defaults to create and keeps them after remount", async () => {
    const first = mountEditor("/a/file.ts");
    await flushPromises();

    expect(editorCreate).toHaveBeenCalledTimes(1);
    expect(editorCreate.mock.calls[0][1]).toMatchObject(
      expectedStableEditorOptions,
    );

    first.unmount();
    await flushPromises();

    const second = mountEditor("/a/file.ts");
    await flushPromises();

    expect(editorCreate).toHaveBeenCalledTimes(2);
    expect(editorCreate.mock.calls[1][1]).toMatchObject(
      expectedStableEditorOptions,
    );

    second.unmount();
    await flushPromises();
  });

  it("keeps stable defaults in updateOptions after editor settings change", async () => {
    const wrapper = mountEditor("/a/file.ts");
    await flushPromises();

    const editor = mountedEditors[0];
    appStateHolder.state.fontSize = 16;
    await flushPromises();

    expect(editor.optionUpdates).toContainEqual(
      expect.objectContaining({
        fontSize: 16,
        ...expectedStableEditorOptions,
      }),
    );

    wrapper.unmount();
    await flushPromises();
  });

  it("applies minimap, sticky scroll and inlay hint settings immediately", async () => {
    const wrapper = mountEditor("/a/file.ts");
    await flushPromises();
    const editor = mountedEditors[0];

    appStateHolder.state.minimapEnabled = true;
    appStateHolder.state.stickyScrollEnabled = false;
    appStateHolder.state.inlayHintsEnabled = false;
    await flushPromises();

    expect(editor.optionUpdates.at(-1)).toMatchObject({
      minimap: { enabled: true },
      stickyScroll: { enabled: false },
      inlayHints: { enabled: "off" },
    });

    wrapper.unmount();
    await flushPromises();
  });
});

describe("editor navigation targeting", () => {
  beforeEach(() => {
    mountedEditors.length = 0;
    appStateHolder.state.editorJumpSeq = 0;
    appStateHolder.state.editorJumpTargetSeq = 0;
    appStateHolder.state.editorJumpTargetPath = null;
    appStateHolder.state.editorJumpTargetGroupId = null;
    appStateHolder.state.cursorLine = 1;
    appStateHolder.state.cursorColumn = 1;
    layoutState.tree.activeLeafId = "group-a";
    setPanelTab.mockClear();
    setSidebarVisible.mockClear();
    setCallHierarchyQuery.mockClear();
  });

  it("consumes a targeted jump only in the matching file and editor group", async () => {
    const matching = mountEditor("C:\\repo\\target.ts", "group-a");
    const wrongGroup = mountEditor("C:\\repo\\target.ts", "group-b");
    const wrongFile = mountEditor("C:\\repo\\other.ts", "group-a");
    await flushPromises();

    appStateHolder.state.cursorLine = 12;
    appStateHolder.state.cursorColumn = 5;
    appStateHolder.state.editorJumpTargetPath = "c:/repo/target.ts";
    appStateHolder.state.editorJumpTargetGroupId = "group-a";
    appStateHolder.state.editorJumpTargetSeq = 1;
    appStateHolder.state.editorJumpSeq = 1;
    await flushPromises();

    expect(mountedEditors[0].setPosition).toHaveBeenCalledWith({
      lineNumber: 12,
      column: 5,
    });
    expect(mountedEditors[1].setPosition).not.toHaveBeenCalled();
    expect(mountedEditors[2].setPosition).not.toHaveBeenCalled();
    matching.unmount();
    wrongGroup.unmount();
    wrongFile.unmount();
    await flushPromises();
  });

  it("routes a legacy jump only to the active split group", async () => {
    const inactive = mountEditor("/repo/a.ts", "group-a");
    const active = mountEditor("/repo/b.ts", "group-b");
    await flushPromises();
    layoutState.tree.activeLeafId = "group-b";

    appStateHolder.state.cursorLine = 9;
    appStateHolder.state.cursorColumn = 2;
    appStateHolder.state.editorJumpSeq = 1;
    await flushPromises();

    expect(mountedEditors[0].setPosition).not.toHaveBeenCalled();
    expect(mountedEditors[1].setPosition).toHaveBeenCalledWith({
      lineNumber: 9,
      column: 2,
    });
    inactive.unmount();
    active.unmount();
    await flushPromises();
  });

  it("opens call hierarchy in the sidebar for the editor instance path", async () => {
    appStateHolder.state.currentFilePath = "/repo/another.ts";
    const wrapper = mountEditor("/repo/own.ts", "group-a");
    await flushPromises();
    const action = mountedEditors[0].actions.find(
      (entry: { id?: string }) => entry.id === "show-call-hierarchy",
    );

    action?.run?.();

    expect(setCallHierarchyQuery).toHaveBeenCalledWith(
      expect.objectContaining({
        mode: "call",
        filePath: "/repo/own.ts",
        line: 2,
        column: 3,
      }),
    );
    expect(setSidebarVisible).toHaveBeenCalledWith(true);
    expect(setPanelTab).toHaveBeenCalledWith("callHierarchy");
    wrapper.unmount();
    appStateHolder.state.currentFilePath = null;
    await flushPromises();
  });
});

describe("debug decoration watch lifecycle", () => {
  it("stops the asynchronously-created debug watch on unmount", async () => {
    mountedEditors.length = 0;
    debugHolder.state.breakpoints = [];
    debugHolder.state.stopped = false;
    debugHolder.state.stack = [];
    const wrapper = mountEditor("/repo/main.go", "group-a");
    await flushPromises();
    await flushPromises();

    wrapper.unmount();
    await flushPromises();
    const callsAfterUnmount = mountedEditors[0].calls.length;
    debugHolder.state.breakpoints.push({ file: "/repo/main.go", line: 3 });
    debugHolder.state.stopped = true;
    debugHolder.state.stack.push({ file: "/repo/main.go", line: 3 });
    await flushPromises();

    expect(mountedEditors[0].calls).toHaveLength(callsAfterUnmount);
  });
});

describe("inline completion provider ownership", () => {
  beforeEach(() => {
    mountedEditors.length = 0;
    inlineProviders.length = 0;
    requestCompletion.mockClear();
    requestCompletion.mockResolvedValue("suggestion");
    cancelInlineCompletion.mockClear();
  });

  it("ignores another editor's model and cancels only its own owner", async () => {
    const wrapperA = mountEditor("/repo/a.ts", "group-a");
    const wrapperB = mountEditor("/repo/b.ts", "group-b");
    await flushPromises();

    expect(inlineProviders).toHaveLength(2);
    const modelA = mountedEditors[0].getModel();
    const modelB = mountedEditors[1].getModel();
    const position = { lineNumber: 1, column: 5 };

    await expect(
      inlineProviders[0].provideInlineCompletions(modelB, position),
    ).resolves.toEqual({ items: [] });
    expect(requestCompletion).not.toHaveBeenCalled();

    await inlineProviders[0].provideInlineCompletions(modelA, position);
    await inlineProviders[1].provideInlineCompletions(modelB, position);
    expect(requestCompletion).toHaveBeenCalledTimes(2);
    expect(requestCompletion.mock.calls[0].slice(2, 4)).toEqual([
      "typescript",
      "/repo/a.ts",
    ]);
    expect(requestCompletion.mock.calls[1].slice(2, 4)).toEqual([
      "typescript",
      "/repo/b.ts",
    ]);
    const ownerA = requestCompletion.mock.calls[0][4];
    const ownerB = requestCompletion.mock.calls[1][4];
    expect(typeof ownerA).toBe("symbol");
    expect(typeof ownerB).toBe("symbol");
    expect(ownerA).not.toBe(ownerB);

    wrapperA.unmount();
    expect(cancelInlineCompletion).toHaveBeenLastCalledWith(ownerA);
    expect(cancelInlineCompletion).not.toHaveBeenCalledWith(ownerB);

    wrapperB.unmount();
    expect(cancelInlineCompletion).toHaveBeenLastCalledWith(ownerB);
    await flushPromises();
  });
});

describe("8C: Emmet editor lifecycle", () => {
  beforeEach(() => {
    mountedEditors.length = 0;
    registerEmmetProviders.mockClear();
    emmetDispose.mockClear();
    appStateHolder.state.emmetEnabled = true;
    appStateHolder.state.emmetIncludeLanguages = { vue: "html" };
  });

  it("registers configured providers without adding a Tab keybinding and disposes on unmount", async () => {
    const wrapper = mountEditor("/a/file.ts");
    await flushPromises();

    expect(registerEmmetProviders).toHaveBeenCalledWith(fakeMonaco, {
      enabled: true,
      includeLanguages: { vue: "html" },
    });
    expect(editorCreate.mock.calls.at(-1)?.[1]).toMatchObject({
      tabCompletion: "on",
    });
    expect(
      mountedEditors[0].actions.some((action: { keybindings?: number[] }) =>
        action.keybindings?.includes(fakeMonaco.KeyCode.Tab),
      ),
    ).toBe(false);

    wrapper.unmount();
    await flushPromises();
    expect(emmetDispose).toHaveBeenCalledTimes(1);
  });

  it("releases the previous Emmet lease when Monaco mounts again", async () => {
    const wrapper = mountEditor("/a/file.ts");
    await flushPromises();

    wrapper
      .findComponent({ name: "VueMonacoEditor" })
      .vm.$emit("mount", mountedEditors[0], fakeMonaco);
    await flushPromises();

    expect(registerEmmetProviders).toHaveBeenCalledTimes(2);
    expect(emmetDispose).toHaveBeenCalledTimes(1);

    wrapper.unmount();
    await flushPromises();
    expect(emmetDispose).toHaveBeenCalledTimes(2);
  });

  it("releases every mount-scoped resource before a Monaco HMR remount", async () => {
    disposableTracker.created = 0;
    disposableTracker.disposed = 0;
    inlineProviderDispose.mockClear();
    lspProviderDisposables.length = 0;
    const wrapper = mountEditor("/a/file.ts");
    await flushPromises();
    await flushPromises();
    const resourcesFromFirstMount = disposableTracker.created;
    const firstLspLease = lspProviderDisposables[0];

    wrapper
      .findComponent({ name: "VueMonacoEditor" })
      .vm.$emit("mount", mountedEditors[0], fakeMonaco);
    await flushPromises();

    expect(inlineProviderDispose).toHaveBeenCalledTimes(1);
    expect(firstLspLease.dispose).toHaveBeenCalledTimes(1);
    expect(disposableTracker.disposed).toBe(resourcesFromFirstMount);
    expect(disposableTracker.created).toBeGreaterThan(resourcesFromFirstMount);

    wrapper.unmount();
    await flushPromises();
    expect(inlineProviderDispose).toHaveBeenCalledTimes(2);
    expect(disposableTracker.disposed).toBe(disposableTracker.created);
  });
});

describe("LSP provider path lifecycle", () => {
  beforeEach(() => {
    mountedEditors.length = 0;
    registerLSPProviders.mockClear();
    lspProviderDisposables.length = 0;
  });

  it("disposes the old providers and registers the new path when path changes", async () => {
    const wrapper = mountEditor("/repo/old.ts");
    await flushPromises();

    expect(registerLSPProviders).toHaveBeenCalledWith(
      fakeMonaco,
      "/repo/old.ts",
    );
    const oldProviders = lspProviderDisposables[0];

    await wrapper.setProps({ path: "/repo/new.ts" });
    await flushPromises();

    expect(oldProviders.dispose).toHaveBeenCalledOnce();
    expect(registerLSPProviders).toHaveBeenLastCalledWith(
      fakeMonaco,
      "/repo/new.ts",
    );
    expect(lspProviderDisposables).toHaveLength(2);

    wrapper.unmount();
    await flushPromises();
  });
});

// 辅助：等待 mountedEditors 中指定实例出现 coverage 装饰调用（newCount=2）。
async function waitForCoverageApplied(
  editor: any,
  expectedCount: number,
  timeout = 2000,
) {
  await vi.waitFor(
    () => {
      expect(editor.calls.some((c: any) => c.newCount === expectedCount)).toBe(
        true,
      );
    },
    { timeout },
  );
}

describe("H-11: CodeEditor 多实例装饰隔离", () => {
  beforeEach(() => {
    mountedEditors.length = 0;
    disposableTracker.created = 0;
    disposableTracker.disposed = 0;
  });

  afterEach(() => {
    // 清理 coverage hits，避免跨用例干扰。
    if (coverageHolder.state) {
      coverageHolder.state.hits.splice(0, coverageHolder.state.hits.length);
    }
  });

  it("两个编辑器实例的 coverage 装饰互不影响", async () => {
    // 逐个挂载并 flush，确保每个实例的 handleMount（含动态 import 与装饰应用）完成。
    // 实例 A 的 path=/a/file.ts 有 2 条 coverage 命中；实例 B 的 path=/b/file.ts 无命中。
    const wrapperA = mountEditor("/a/file.ts");
    // 等待 A 的 applyCoverageDecorations（内部 void import().then()）完成。
    await waitForCoverageApplied(mountedEditors[0], 2);

    const wrapperB = mountEditor("/b/file.ts");
    await flushPromises();
    await flushPromises();
    await flushPromises();

    const editorA = mountedEditors[0];
    const editorB = mountedEditors[1];
    expect(editorA).toBeDefined();
    expect(editorB).toBeDefined();

    // 实例 A 命中 2 条 coverage → 存在一次 newCount=2 的 deltaDecorations 调用。
    expect(editorA.calls.some((c: any) => c.newCount === 2)).toBe(true);

    // 核心断言：实例 B 的所有 deltaDecorations 调用的 old 永远为空。
    // 若 coverageDecorations 是模块级 let（共享），B 挂载时 A 已应用了 2 条装饰，
    // B 的 deltaDecorations 会拿到 A 的 2 个 ID 作为 old（old.length === 2）。
    // H-11 修复后 coverageDecorations 是 per-instance ref，B 的 old 始终为 []。
    const bHasNonEmptyOld = editorB.calls.some((c: any) => c.old.length > 0);
    expect(bHasNonEmptyOld).toBe(false);

    // 验证 reactivity：coverageHolder.state 是 reactive 对象，watch 能检测到 hits 变化。
    // 这是组件内 watch(coverageState.hits.length, ...) 能正常工作的前提。
    let directWatchFired = false;
    const stopWatch = watch(
      () => coverageHolder.state?.hits?.length,
      () => {
        directWatchFired = true;
      },
    );
    coverageHolder.state.hits.push({});
    await flushPromises();
    stopWatch();
    expect(directWatchFired).toBe(true);

    wrapperA.unmount();
    wrapperB.unmount();
    // 额外 flush 以消化卸载后残留的微任务，避免 EnvironmentTeardownError。
    await flushPromises();
  });
});

describe("8D: Istanbul coverage decoration lifecycle", () => {
  beforeEach(() => {
    mountedEditors.length = 0;
  });

  it("renders partial coverage with a distinct gutter and overview marker", async () => {
    const wrapper = mountEditor("/partial/file.ts");
    await waitForCoverageApplied(mountedEditors[0], 1);

    const coverageCall = mountedEditors[0].calls.find(
      (call: any) => call.newCount === 1,
    );
    expect(coverageCall.lineClasses).toContain("coverage-line--partial");
    expect(coverageCall.gutterClasses).toContain("coverage-gutter--partial");
    expect(coverageCall.overviewColors).toContain("rgba(210,153,34,0.72)");

    wrapper.unmount();
    await flushPromises();
  });

  it("clears old coverage decorations when the editor path changes", async () => {
    const wrapper = mountEditor("/a/file.ts");
    const editor = mountedEditors[0];
    await waitForCoverageApplied(editor, 2);

    await wrapper.setProps({ path: "/b/file.ts" });
    await vi.waitFor(() => {
      expect(
        editor.calls.some(
          (call: any) => call.old.length === 2 && call.newCount === 0,
        ),
      ).toBe(true);
    });

    wrapper.unmount();
    await flushPromises();
  });
});

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

describe("8B: current-line inline blame", () => {
  beforeEach(() => {
    mountedEditors.length = 0;
    gitGetBlame.mockReset();
    gitGetBlame.mockResolvedValue([]);
    gitGetBlameForRange.mockReset();
    gitGetBlameForRange.mockResolvedValue([]);
    appStateHolder.state.gitBlameEnabled = true;
    appStateHolder.state.currentProject = "/repo";
    appStateHolder.state.workspaceFolders = ["/repo"];
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-07-16T02:00:00Z"));
  });

  afterEach(() => {
    appStateHolder.state.gitBlameEnabled = false;
    appStateHolder.state.currentProject = null;
    appStateHolder.state.workspaceFolders = [];
    vi.useRealTimers();
  });

  it("debounces cursor changes and requests only the current line", async () => {
    const wrapper = mountEditor("/repo/main.go");
    await flushPromises();
    const editor = mountedEditors[0];
    gitGetBlameForRange.mockClear();

    editor.cursorChangeCallbacks[0]({ position: { lineNumber: 7, column: 3 } });
    await vi.advanceTimersByTimeAsync(249);
    expect(gitGetBlameForRange).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);

    expect(gitGetBlameForRange).toHaveBeenCalledWith("/repo", "main.go", 7, 7);
    expect(gitGetBlame).not.toHaveBeenCalled();
    wrapper.unmount();
    await flushPromises();
  });

  it("falls back to the legacy blame service when the cached binding is unavailable", async () => {
    gitGetBlameForRange.mockRejectedValueOnce(new Error("unknown binding"));
    gitGetBlame.mockResolvedValueOnce([]);
    const wrapper = mountEditor("/repo/main.go");
    await flushPromises();
    gitGetBlameForRange.mockClear();
    gitGetBlame.mockClear();

    mountedEditors[0].cursorChangeCallbacks[0]({
      position: { lineNumber: 4, column: 1 },
    });
    await vi.advanceTimersByTimeAsync(250);
    await flushPromises();

    expect(gitGetBlameForRange).toHaveBeenCalledWith("/repo", "main.go", 4, 4);
    expect(gitGetBlame).toHaveBeenCalledWith("/repo", "main.go", 4, 4, "");
    wrapper.unmount();
    await flushPromises();
  });

  it("normalizes Windows paths case-insensitively before calling blame", async () => {
    appStateHolder.state.currentProject = "C:\\Work\\Repo";
    appStateHolder.state.workspaceFolders = ["C:\\Work\\Repo"];
    const wrapper = mountEditor("c:/work\\repo/src\\main.go");
    await flushPromises();
    gitGetBlameForRange.mockClear();

    mountedEditors[0].cursorChangeCallbacks[0]({
      position: { lineNumber: 5, column: 1 },
    });
    await vi.advanceTimersByTimeAsync(250);

    expect(gitGetBlameForRange).toHaveBeenCalledWith(
      "C:\\Work\\Repo",
      "src/main.go",
      5,
      5,
    );
    wrapper.unmount();
    await flushPromises();
  });

  it("does not request blame for files outside the active repository", async () => {
    const wrapper = mountEditor("/other/main.go");
    await flushPromises();
    gitGetBlameForRange.mockClear();
    gitGetBlame.mockClear();

    mountedEditors[0].cursorChangeCallbacks[0]({
      position: { lineNumber: 3, column: 1 },
    });
    await vi.advanceTimersByTimeAsync(250);

    expect(gitGetBlameForRange).not.toHaveBeenCalled();
    expect(gitGetBlame).not.toHaveBeenCalled();
    wrapper.unmount();
    await flushPromises();
  });

  it.each([
    ["POSIX sibling", "/repo-copy/main.go"],
    ["POSIX case mismatch", "/Repo/main.go"],
    ["drive-relative", "C:main.go"],
    ["relative traversal", "../main.go"],
    ["absolute traversal", "/repo/src/../../../main.go"],
    ["file URI", "file:///repo/main.go"],
    ["NUL byte", "/repo/main\0.go"],
  ])("rejects unsafe or non-member paths: %s", async (_name, path) => {
    const wrapper = mountEditor(path);
    await flushPromises();
    gitGetBlameForRange.mockClear();

    mountedEditors
      .at(-1)
      .cursorChangeCallbacks[0]({ position: { lineNumber: 3, column: 1 } });
    await vi.advanceTimersByTimeAsync(250);

    expect(gitGetBlameForRange).not.toHaveBeenCalled();
    wrapper.unmount();
    await flushPromises();
  });

  it("accepts a safe repository-relative path", async () => {
    const wrapper = mountEditor("src/../main.go");
    await flushPromises();
    gitGetBlameForRange.mockClear();

    mountedEditors[0].cursorChangeCallbacks[0]({
      position: { lineNumber: 6, column: 1 },
    });
    await vi.advanceTimersByTimeAsync(250);

    expect(gitGetBlameForRange).toHaveBeenCalledWith("/repo", "main.go", 6, 6);
    wrapper.unmount();
    await flushPromises();
  });

  it("rejects a Windows path on another drive or UNC share", async () => {
    appStateHolder.state.currentProject = "C:\\repo";
    const driveWrapper = mountEditor("D:\\repo\\main.go");
    await flushPromises();
    gitGetBlameForRange.mockClear();
    mountedEditors[0].cursorChangeCallbacks[0]({
      position: { lineNumber: 2, column: 1 },
    });
    await vi.advanceTimersByTimeAsync(250);
    expect(gitGetBlameForRange).not.toHaveBeenCalled();
    driveWrapper.unmount();

    appStateHolder.state.currentProject = "\\\\server\\share\\repo";
    const uncWrapper = mountEditor("\\\\server\\other\\repo\\main.go");
    await flushPromises();
    gitGetBlameForRange.mockClear();
    mountedEditors
      .at(-1)
      .cursorChangeCallbacks[0]({ position: { lineNumber: 2, column: 1 } });
    await vi.advanceTimersByTimeAsync(250);
    expect(gitGetBlameForRange).not.toHaveBeenCalled();
    uncWrapper.unmount();
    await flushPromises();
  });

  it("waits for the primary root of a .code-workspace and rejects secondary roots", async () => {
    appStateHolder.state.currentProject = "/work/project.code-workspace";
    appStateHolder.state.workspaceFolders = [];
    const primary = mountEditor("/repo/main.go");
    await flushPromises();
    gitGetBlameForRange.mockClear();
    mountedEditors[0].cursorChangeCallbacks[0]({
      position: { lineNumber: 8, column: 1 },
    });
    await vi.advanceTimersByTimeAsync(250);
    expect(gitGetBlameForRange).not.toHaveBeenCalled();

    appStateHolder.state.workspaceFolders = ["/repo", "/secondary"];
    await flushPromises();
    await vi.advanceTimersByTimeAsync(250);
    expect(gitGetBlameForRange).toHaveBeenCalledWith("/repo", "main.go", 1, 1);
    primary.unmount();
    await flushPromises();

    gitGetBlameForRange.mockClear();
    const secondary = mountEditor("/secondary/other.go");
    await flushPromises();
    mountedEditors
      .at(-1)
      .cursorChangeCallbacks[0]({ position: { lineNumber: 9, column: 1 } });
    await vi.advanceTimersByTimeAsync(250);
    expect(gitGetBlameForRange).not.toHaveBeenCalled();
    secondary.unmount();
    await flushPromises();
  });

  it("ignores an in-flight response after the authorized repository root changes", async () => {
    const pending = deferred<Array<Record<string, unknown>>>();
    gitGetBlameForRange.mockImplementationOnce(() => pending.promise);
    const wrapper = mountEditor("/repo/main.go");
    await flushPromises();
    const editor = mountedEditors[0];
    gitGetBlameForRange.mockClear();

    editor.cursorChangeCallbacks[0]({ position: { lineNumber: 2, column: 1 } });
    await vi.advanceTimersByTimeAsync(250);
    appStateHolder.state.currentProject = "/next";
    appStateHolder.state.workspaceFolders = ["/next"];
    await flushPromises();
    pending.resolve([
      {
        line: 2,
        commit: "aaaaaaaa",
        author: "Old Root",
        time: "2026-07-16T00:00:00Z",
        summary: "stale root",
      },
    ]);
    await flushPromises();

    const rendered = editor.calls.flatMap(
      (call: { afterContents: Array<string | undefined> }) =>
        call.afterContents.filter(Boolean),
    );
    expect(rendered).not.toContain(expect.stringContaining("Old Root"));
    wrapper.unmount();
    await flushPromises();
  });

  it("renders relative time and ignores a late response from an older cursor line", async () => {
    const wrapper = mountEditor("/repo/main.go");
    await flushPromises();
    const editor = mountedEditors[0];
    const first = deferred<Array<Record<string, unknown>>>();
    const second = deferred<Array<Record<string, unknown>>>();
    gitGetBlameForRange.mockReset();
    gitGetBlameForRange
      .mockImplementationOnce(() => first.promise)
      .mockImplementationOnce(() => second.promise);

    editor.cursorChangeCallbacks[0]({ position: { lineNumber: 2, column: 1 } });
    await vi.advanceTimersByTimeAsync(250);
    editor.cursorChangeCallbacks[0]({ position: { lineNumber: 3, column: 1 } });
    await vi.advanceTimersByTimeAsync(250);

    second.resolve([
      {
        line: 3,
        commit: "bbbbbbbb",
        author: "New Author",
        email: "new@example.com",
        time: "2026-07-16T00:00:00Z",
        summary: "latest change",
      },
    ]);
    await flushPromises();

    const blameCalls = () =>
      editor.calls.filter(
        (call: { afterContents: Array<string | undefined> }) =>
          call.afterContents.some(Boolean),
      );
    expect(blameCalls().at(-1)?.afterContents[0]).toContain(
      "New Author · 2h ago · latest change",
    );
    expect(blameCalls().at(-1)?.hoverMessages[0]).toContain("`bbbbbbbb`");
    expect(blameCalls().at(-1)?.hoverMessages[0]).toContain(
      "New Author <new@example.com>",
    );
    expect(blameCalls().at(-1)?.hoverMessages[0]).toContain("latest change");
    const committedCallCount = blameCalls().length;

    first.resolve([
      {
        line: 2,
        commit: "aaaaaaaa",
        author: "Old Author",
        email: "old@example.com",
        time: "2026-07-15T00:00:00Z",
        summary: "stale change",
      },
    ]);
    await flushPromises();

    expect(blameCalls()).toHaveLength(committedCallCount);
    expect(blameCalls().at(-1)?.afterContents[0]).not.toContain("Old Author");
    wrapper.unmount();
    await flushPromises();
  });
});

describe("inline blame content changes", () => {
  beforeEach(() => {
    mountedEditors.length = 0;
    gitGetBlameForRange.mockReset();
    gitGetBlameForRange.mockResolvedValue([]);
    appStateHolder.state.gitBlameEnabled = true;
    appStateHolder.state.currentProject = "/repo";
    appStateHolder.state.workspaceFolders = ["/repo"];
    disposableTracker.created = 0;
    disposableTracker.disposed = 0;
    vi.useFakeTimers();
  });

  afterEach(() => {
    appStateHolder.state.gitBlameEnabled = false;
    appStateHolder.state.currentProject = null;
    appStateHolder.state.workspaceFolders = [];
    vi.useRealTimers();
  });

  it("does not schedule repository requests for every edit", async () => {
    const wrapper = mountEditor("/repo/file.ts");
    await flushPromises();
    await flushPromises();

    const editor = mountedEditors[0];
    await vi.advanceTimersByTimeAsync(250);
    gitGetBlameForRange.mockClear();

    expect(editor.contentChangeCallbacks).toHaveLength(0);
    await vi.advanceTimersByTimeAsync(500);
    expect(gitGetBlameForRange).not.toHaveBeenCalled();

    editor.cursorChangeCallbacks[0]({ position: { lineNumber: 3, column: 1 } });
    await vi.advanceTimersByTimeAsync(250);
    expect(gitGetBlameForRange).toHaveBeenCalledWith("/repo", "file.ts", 3, 3);

    wrapper.unmount();
    await flushPromises();
  });
});

// M-12: 验证 editor.addAction / onDid* 返回的 IDisposable 在卸载时被释放。
describe("M-12: addAction disposables 在卸载时释放", () => {
  beforeEach(() => {
    mountedEditors.length = 0;
    disposableTracker.created = 0;
    disposableTracker.disposed = 0;
  });

  it("挂载创建 disposables，卸载后全部释放", async () => {
    const wrapper = mountEditor("/a/file.ts");
    await flushPromises();
    await flushPromises();

    // 挂载后应创建多个 IDisposable（addAction、cursor、mouse 等）。
    expect(disposableTracker.created).toBeGreaterThan(0);
    // 卸载前还没有释放任何 disposable。
    expect(disposableTracker.disposed).toBe(0);

    wrapper.unmount();
    await flushPromises();

    // 卸载后所有创建的 disposable 都应被释放。
    expect(disposableTracker.disposed).toBe(disposableTracker.created);
  });
});
