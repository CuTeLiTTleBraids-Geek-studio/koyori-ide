import { flushPromises, mount } from "@vue/test-utils";
import {
  afterEach,
  beforeEach,
  describe,
  expect,
  it,
  type Mock,
  vi,
} from "vitest";
import {
  createLeaf,
  DEFAULT_SPLIT_RATIO,
  deserializeLayout,
  findLeaf,
  getAllLeaves,
  layoutNodeKey,
  removeLeaf,
  resize,
  resizeSplit,
  serializeLayout,
  splitLeaf,
  type LayoutNode,
  type LeafNode,
  type SplitNode,
} from "./terminalSplit";
import { isDebugThreadsActive } from "./debugThreads";

interface MonacoModelMock {
  getValue: Mock<() => string>;
  setValue: ReturnType<typeof vi.fn>;
  onDidChangeContent: ReturnType<typeof vi.fn>;
  deltaDecorations: ReturnType<typeof vi.fn>;
  dispose: ReturnType<typeof vi.fn>;
  listenerDispose: ReturnType<typeof vi.fn>;
}

interface MonacoDiffEditorMock {
  setModel: ReturnType<typeof vi.fn>;
  dispose: ReturnType<typeof vi.fn>;
  layout: ReturnType<typeof vi.fn>;
  updateOptions: ReturnType<typeof vi.fn>;
  getModifiedEditor: ReturnType<typeof vi.fn>;
}

const componentMocks = vi.hoisted(() => ({
  terminalInstances: [] as Array<{
    options: Record<string, unknown>;
    loadAddon: ReturnType<typeof vi.fn>;
    open: ReturnType<typeof vi.fn>;
    write: ReturnType<typeof vi.fn>;
    reset: ReturnType<typeof vi.fn>;
    focus: ReturnType<typeof vi.fn>;
    dispose: ReturnType<typeof vi.fn>;
    dataListener?: (data: string) => void;
    resizeListener?: (size: { cols: number; rows: number }) => void;
  }>,
  fitInstances: [] as Array<{ fit: ReturnType<typeof vi.fn> }>,
  streamListeners: new Map<string, (data: string) => void>(),
  unsubscribeMocks: [] as Array<ReturnType<typeof vi.fn>>,
  writeSessionMock: vi.fn(),
  resizeSessionMock: vi.fn(),
  subscribeOutputMock: vi.fn(),
}));

const integrationMocks = vi.hoisted(() => ({
  runtimeEventsOn: vi.fn(() => vi.fn()),
  messageSuccess: vi.fn(),
  messageError: vi.fn(),
  messageBoxConfirm: vi.fn().mockResolvedValue("confirm"),
  messageBoxPrompt: vi.fn().mockResolvedValue({ value: "maintenance" }),
}));

const monacoMocks = vi.hoisted(() => ({
  models: [] as MonacoModelMock[],
  editors: [] as MonacoDiffEditorMock[],
  createModel: vi.fn(),
  createDiffEditor: vi.fn(),
  setModelLanguage: vi.fn(),
}));

vi.mock("@wailsio/runtime", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@wailsio/runtime")>();
  return {
    ...actual,
    Events: { ...actual.Events, On: integrationMocks.runtimeEventsOn },
  };
});

vi.mock("element-plus", async (importOriginal) => {
  const actual = await importOriginal<typeof import("element-plus")>();
  return {
    ...actual,
    ElMessage: {
      success: integrationMocks.messageSuccess,
      error: integrationMocks.messageError,
    },
    ElMessageBox: {
      ...actual.ElMessageBox,
      confirm: integrationMocks.messageBoxConfirm,
      prompt: integrationMocks.messageBoxPrompt,
    },
  };
});

vi.mock("monaco-editor", () => ({
  Range: class MockRange {
    constructor(
      public startLineNumber: number,
      public startColumn: number,
      public endLineNumber: number,
      public endColumn: number,
    ) {}
  },
  editor: {
    OverviewRulerLane: { Full: 7 },
    createModel: monacoMocks.createModel,
    createDiffEditor: monacoMocks.createDiffEditor,
    setModelLanguage: monacoMocks.setModelLanguage,
    defineTheme: vi.fn(),
    setTheme: vi.fn(),
  },
  languages: {
    register: vi.fn(),
    setMonarchTokensProvider: vi.fn(),
    registerCompletionItemProvider: vi.fn(() => ({ dispose: vi.fn() })),
  },
  KeyMod: { CtrlCmd: 2048, Shift: 1024, Alt: 512, WinCtrl: 256 },
  KeyCode: {
    KeyA: 31,
    KeyS: 49,
    Enter: 3,
    Escape: 9,
    Backslash: 88,
  },
}));

vi.mock("@xterm/xterm", () => ({
  Terminal: class MockTerminal {
    options: Record<string, unknown>;
    loadAddon = vi.fn();
    open = vi.fn();
    write = vi.fn();
    reset = vi.fn();
    focus = vi.fn();
    dispose = vi.fn();
    dataListener?: (data: string) => void;
    resizeListener?: (size: { cols: number; rows: number }) => void;

    constructor(options: Record<string, unknown>) {
      this.options = options;
      componentMocks.terminalInstances.push(this);
    }

    onData(listener: (data: string) => void) {
      this.dataListener = listener;
      return { dispose: vi.fn() };
    }

    onResize(listener: (size: { cols: number; rows: number }) => void) {
      this.resizeListener = listener;
      return { dispose: vi.fn() };
    }
  },
}));

vi.mock("@xterm/addon-fit", () => ({
  FitAddon: class MockFitAddon {
    fit = vi.fn();

    constructor() {
      componentMocks.fitInstances.push(this);
    }
  },
}));

vi.mock("@/stores/app", () => ({
  appState: {
    terminalFontSize: 13,
    terminalCursorStyle: "block",
    scrollback: 5000,
    theme: "dark",
    fontFamily: "monospace",
    fontSize: 13,
    wordWrap: false,
    accentTheme: "blue",
  },
}));

vi.mock("@/stores/terminal", () => ({
  writeToSession: componentMocks.writeSessionMock,
  resizeSession: componentMocks.resizeSessionMock,
  onTerminalOutput: componentMocks.subscribeOutputMock,
}));

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({
    t: (key: string) => key,
    locale: { value: "en" },
  }),
}));

import TerminalSplitPane from "@/components/layout/TerminalSplitPane.vue";
import WorktreePanel from "@/components/git/WorktreePanel.vue";
import MergeEditor, {
  createConflictRegionResult,
  createInitialThreeWayResult,
  hasConflictMarkers,
  joinRepoFilePath,
  parseConflictBlocks,
  resolveConflictBlock,
  stabilizeConflictBlockIds,
} from "@/components/git/MergeEditor.vue";
import {
  activateAllFeatures,
  activateFeature,
  deactivateAllFeatures,
  deactivateFeature,
  getFeatureActivationError,
  getFeatureComponentLoader,
  isFeatureActive,
  listFeatures,
  loadFeatureComponent,
  registerFeature,
  setFeatureComponentLoader,
  unregisterFeature,
} from "./featureRegistry";

function leaf(sessionId: string): LeafNode {
  return createLeaf(sessionId);
}

function resetMonacoMocks(): void {
  monacoMocks.models.length = 0;
  monacoMocks.editors.length = 0;
  monacoMocks.setModelLanguage.mockReset();
  monacoMocks.createModel.mockReset().mockImplementation((initialValue: string) => {
    let value = initialValue;
    let contentListener: (() => void) | null = null;
    const listenerDispose = vi.fn(() => {
      contentListener = null;
    });
    const model: MonacoModelMock = {
      getValue: vi.fn(() => value),
      setValue: vi.fn((nextValue: string) => {
        value = nextValue;
        contentListener?.();
      }),
      onDidChangeContent: vi.fn((listener: () => void) => {
        contentListener = listener;
        return { dispose: listenerDispose };
      }),
      deltaDecorations: vi.fn(
        (_oldDecorations: string[], decorations: unknown[]) =>
          decorations.map((_decoration, index) => `decoration-${index}`),
      ),
      dispose: vi.fn(),
      listenerDispose,
    };
    monacoMocks.models.push(model);
    return model;
  });
  monacoMocks.createDiffEditor.mockReset().mockImplementation(() => {
    const modifiedEditor = {
      revealLineInCenter: vi.fn(),
      setPosition: vi.fn(),
      focus: vi.fn(),
    };
    const editor: MonacoDiffEditorMock = {
      setModel: vi.fn(),
      dispose: vi.fn(),
      layout: vi.fn(),
      updateOptions: vi.fn(),
      getModifiedEditor: vi.fn(() => modifiedEditor),
    };
    monacoMocks.editors.push(editor);
    return editor;
  });
}

describe("terminal split layout", () => {
  it("splits a leaf without mutating or replacing the existing leaf", () => {
    const original = leaf("session-a");
    const result = splitLeaf(original, "horizontal", "session-b");

    expect(result).toMatchObject({
      type: "split",
      direction: "horizontal",
      ratio: DEFAULT_SPLIT_RATIO,
    });
    expect(result.children[0]).toBe(original);
    expect(result.children[1]).toMatchObject({
      type: "leaf",
      sessionId: "session-b",
    });
    expect(result.id).toMatch(/^split:/);
    expect(result.children[1].id).toMatch(/^leaf:/);
    expect(original).toMatchObject({ type: "leaf", sessionId: "session-a" });
  });

  it("supports vertical splits", () => {
    expect(splitLeaf(leaf("one"), "vertical", "two").direction).toBe(
      "vertical",
    );
  });

  it("returns null when the final leaf is removed", () => {
    expect(removeLeaf(leaf("only"), "only")).toBeNull();
  });

  it("promotes the sibling when either direct child is removed", () => {
    const first = leaf("first");
    const second = leaf("second");
    const tree: SplitNode = {
      type: "split",
      id: "split:direct",
      direction: "horizontal",
      children: [first, second],
      ratio: 0.35,
    };

    expect(removeLeaf(tree, "first")).toBe(second);
    expect(removeLeaf(tree, "second")).toBe(first);
    expect(tree.children).toEqual([first, second]);
  });

  it("collapses nested splits and preserves untouched branches", () => {
    const first = leaf("first");
    const second = leaf("second");
    const untouched = leaf("untouched");
    const nested = splitLeaf(first, "vertical", second.sessionId);
    const root: SplitNode = {
      type: "split",
      id: "split:root",
      direction: "horizontal",
      children: [nested, untouched],
      ratio: 0.6,
    };

    const result = removeLeaf(root, "second") as SplitNode;

    expect(result).not.toBe(root);
    expect(result.children[0]).toBe(first);
    expect(result.children[1]).toBe(untouched);
    expect(nested.children[1]).toMatchObject({
      type: "leaf",
      sessionId: second.sessionId,
    });
  });

  it("returns the original tree when the session is absent", () => {
    const tree = splitLeaf(leaf("first"), "horizontal", "second");
    expect(removeLeaf(tree, "missing")).toBe(tree);
  });

  it("removes only the first match if malformed input contains duplicates", () => {
    const first = leaf("duplicate");
    const second = leaf("duplicate");
    const tree: SplitNode = {
      type: "split",
      id: "split:duplicates",
      direction: "horizontal",
      children: [first, second],
      ratio: 0.5,
    };

    expect(removeLeaf(tree, "duplicate")).toBe(second);
  });

  it("resizes immutably and clamps ratios to the valid range", () => {
    const tree = splitLeaf(leaf("first"), "horizontal", "second");

    const resized = resize(tree, 0.7);
    expect(resized).not.toBe(tree);
    expect(resized.ratio).toBe(0.7);
    expect(resized.children).toBe(tree.children);
    expect(tree.ratio).toBe(DEFAULT_SPLIT_RATIO);
    expect(resize(tree, -2).ratio).toBe(0);
    expect(resize(tree, 4).ratio).toBe(1);
  });

  it("keeps identity for no-op and non-finite resize requests", () => {
    const tree = splitLeaf(leaf("first"), "horizontal", "second");
    expect(resize(tree, tree.ratio)).toBe(tree);
    expect(resize(tree, Number.NaN)).toBe(tree);
    expect(resize(tree, Number.POSITIVE_INFINITY)).toBe(tree);
  });

  it("builds deterministic, order-sensitive keys from stable session IDs", () => {
    const left: LayoutNode = splitLeaf(
      leaf("first"),
      "vertical",
      "second",
    );
    const right: LayoutNode = {
      type: "split",
      id: "split:reversed",
      direction: "vertical",
      children: [leaf("second"), leaf("first")],
      ratio: 0.5,
    };

    expect(layoutNodeKey(left)).toBe(layoutNodeKey(left));
    expect(layoutNodeKey(left)).not.toBe(layoutNodeKey(right));
    const keyedLeaf = leaf("session-id");
    expect(layoutNodeKey(keyedLeaf)).toBe(keyedLeaf.id);
  });

  it("supports the id-based tree API and preserves untouched branches", () => {
    const first = leaf("first");
    const untouched = leaf("untouched");
    const root: SplitNode = {
      type: "split",
      id: "split:root-api",
      direction: "horizontal",
      children: [first, untouched],
      ratio: 0.5,
    };

    const result = splitLeaf(root, first.id, "vertical", "new-session") as SplitNode;

    expect(result).not.toBe(root);
    expect(result.children[1]).toBe(untouched);
    expect(result.children[0]).toMatchObject({
      type: "split",
      direction: "vertical",
    });
    expect(getAllLeaves(result).map((item) => item.sessionId)).toEqual([
      "first",
      "new-session",
      "untouched",
    ]);
    expect(findLeaf(result, "new-session")?.sessionId).toBe("new-session");
    expect(splitLeaf(root, "missing", "horizontal", "unused")).toBe(root);
  });

  it("resizes nested splits by id and serializes a validated round trip", () => {
    const nested = splitLeaf(leaf("first"), "vertical", "second");
    const root: SplitNode = {
      type: "split",
      id: "split:serialize-root",
      direction: "horizontal",
      children: [nested, leaf("third")],
      ratio: 0.4,
    };

    const resized = resizeSplit(root, nested.id, 0.75) as SplitNode;
    expect(resized).not.toBe(root);
    expect((resized.children[0] as SplitNode).ratio).toBe(0.75);
    expect(resized.children[1]).toBe(root.children[1]);
    expect(resizeSplit(root, "missing", 0.2)).toBe(root);

    const restored = deserializeLayout(serializeLayout(resized));
    expect(restored).toEqual(resized);
    expect(deserializeLayout("not json")).toBeNull();
    expect(
      deserializeLayout(
        JSON.stringify({
          type: "split",
          id: "bad-ratio",
          direction: "horizontal",
          ratio: 2,
          children: [leaf("bad-first"), leaf("bad-second")],
        }),
      ),
    ).toBeNull();
    expect(
      deserializeLayout(JSON.stringify({
        type: "split",
        id: "duplicate-session-root",
        direction: "horizontal",
        ratio: 0.5,
        children: [
          { type: "leaf", id: "duplicate-a", sessionId: "same-session" },
          { type: "leaf", id: "duplicate-b", sessionId: "same-session" },
        ],
      })),
    ).toBeNull();
  });
});

describe("TerminalSplitPane", () => {
  beforeEach(() => {
    componentMocks.terminalInstances.length = 0;
    componentMocks.fitInstances.length = 0;
    componentMocks.streamListeners.clear();
    componentMocks.unsubscribeMocks.length = 0;
    componentMocks.writeSessionMock.mockReset();
    componentMocks.resizeSessionMock.mockReset();
    componentMocks.subscribeOutputMock.mockReset();
    componentMocks.writeSessionMock.mockImplementation(() => Promise.resolve());
    componentMocks.resizeSessionMock.mockImplementation(() =>
      Promise.resolve(),
    );
    componentMocks.subscribeOutputMock.mockImplementation(
      (sessionId: string, listener: (data: string) => void) => {
        componentMocks.streamListeners.set(sessionId, listener);
        const unsubscribe = vi.fn(() => {
          componentMocks.streamListeners.delete(sessionId);
        });
        componentMocks.unsubscribeMocks.push(unsubscribe);
        return unsubscribe;
      },
    );
    vi.stubGlobal(
      "ResizeObserver",
      class MockResizeObserver {
        observe = vi.fn();
        disconnect = vi.fn();
      },
    );
  });

  afterEach(() => {
    document.body.innerHTML = "";
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("owns one xterm lifecycle and forwards leaf controls", async () => {
    const wrapper = mount(TerminalSplitPane, {
      attachTo: document.body,
      props: {
        node: leaf("session-a"),
        sessions: {
          "session-a": { id: "session-a", output: "ready\r\n" },
        },
        activeSessionId: "session-a",
      },
    });

    await wrapper.vm.$nextTick();
    const terminal = componentMocks.terminalInstances[0];
    expect(terminal).toBeDefined();
    expect(terminal.open).toHaveBeenCalledTimes(1);
    expect(terminal.write).toHaveBeenCalledWith("ready\r\n");
    expect(componentMocks.subscribeOutputMock).toHaveBeenCalledWith(
      "session-a",
      expect.any(Function),
    );

    terminal.dataListener?.("pwd\r");
    terminal.resizeListener?.({ cols: 120, rows: 32 });
    expect(componentMocks.writeSessionMock).toHaveBeenCalledWith(
      "session-a",
      "pwd\r",
    );
    expect(componentMocks.resizeSessionMock).toHaveBeenCalledWith(
      "session-a",
      120,
      32,
    );

    componentMocks.streamListeners.get("session-a")?.("next\r\n");
    expect(terminal.write).toHaveBeenLastCalledWith("next\r\n");

    await wrapper.get('[data-action="split-horizontal"]').trigger("click");
    await wrapper.get('[data-action="split-vertical"]').trigger("click");
    await wrapper.get('[data-action="close"]').trigger("click");
    await wrapper.get(".terminal-split-pane__terminal").trigger("mousedown");
    expect(wrapper.emitted("split")).toEqual([
      ["session-a", "horizontal"],
      ["session-a", "vertical"],
    ]);
    expect(wrapper.emitted("close")).toEqual([["session-a"]]);
    expect(wrapper.emitted("activate")).toEqual([["session-a"]]);

    const splitButton = wrapper.get('[data-action="split-horizontal"]');
    const terminalFocusCount = terminal.focus.mock.calls.length;
    (splitButton.element as HTMLButtonElement).focus();
    await wrapper.vm.$nextTick();
    expect(document.activeElement).toBe(splitButton.element);
    expect(terminal.focus).toHaveBeenCalledTimes(terminalFocusCount);
    expect(wrapper.emitted("activate")).toEqual([
      ["session-a"],
      ["session-a"],
    ]);

    wrapper.unmount();
    expect(terminal.dispose).toHaveBeenCalledTimes(1);
    expect(componentMocks.unsubscribeMocks[0]).toHaveBeenCalledTimes(1);
  });

  it("offers split and close commands through an accessible context menu", async () => {
    const wrapper = mount(TerminalSplitPane, {
      attachTo: document.body,
      props: {
        node: leaf("context-session"),
        sessions: {
          "context-session": { id: "context-session", output: "" },
        },
      },
    });
    await wrapper.vm.$nextTick();

    await wrapper.get(".terminal-split-pane--leaf").trigger("contextmenu", {
      clientX: 24,
      clientY: 18,
    });
    const menu = wrapper.get('[role="menu"]');
    const items = menu.findAll('[role="menuitem"]');
    expect(items).toHaveLength(3);

    await items[0].trigger("click");
    expect(wrapper.emitted("split")).toEqual([
      ["context-session", "horizontal"],
    ]);
    expect(wrapper.find('[role="menu"]').exists()).toBe(false);

    const returnButton = wrapper.get<HTMLButtonElement>('[data-action="close"]');
    returnButton.element.focus();
    await wrapper.get(".terminal-split-pane--leaf").trigger("keydown", {
      key: "F10",
      shiftKey: true,
    });
    await wrapper.vm.$nextTick();
    expect(wrapper.get('[role="menu"]')).toBeDefined();
    expect((document.activeElement as HTMLElement | null)?.getAttribute("role")).toBe(
      "menuitem",
    );
    const keyboardMenu = wrapper.get('[role="menu"]');
    await keyboardMenu.trigger("keydown", { key: "ArrowDown" });
    expect(
      keyboardMenu.findAll('[role="menuitem"]')[1].element,
    ).toBe(document.activeElement);
    await keyboardMenu.trigger("keydown", { key: "Escape" });
    await wrapper.vm.$nextTick();
    expect(wrapper.find('[role="menu"]').exists()).toBe(false);
    expect(document.activeElement).toBe(returnButton.element);
    wrapper.unmount();
  });

  it("does not duplicate custom stream data when its snapshot updates later", async () => {
    let streamListener: ((data: string) => void) | null = null;
    const subscribeOutput = vi.fn(
      (_sessionId: string, listener: (data: string) => void) => {
        streamListener = listener;
        return vi.fn();
      },
    );
    const wrapper = mount(TerminalSplitPane, {
      props: {
        node: leaf("custom-stream"),
        sessions: {
          "custom-stream": { id: "custom-stream", output: "ready\r\n" },
        },
        subscribeOutput,
      },
    });
    await wrapper.vm.$nextTick();

    const terminal = componentMocks.terminalInstances[0];
    terminal.write.mockClear();
    const emitStream = streamListener as ((data: string) => void) | null;
    expect(emitStream).not.toBeNull();
    emitStream?.("next\r\n");
    expect(terminal.write).toHaveBeenCalledTimes(1);
    expect(terminal.write).toHaveBeenLastCalledWith("next\r\n");

    emitStream?.("later\r\n");
    expect(terminal.write).toHaveBeenCalledTimes(2);
    expect(terminal.write).toHaveBeenLastCalledWith("later\r\n");

    await wrapper.setProps({
      sessions: {
        "custom-stream": {
          id: "custom-stream",
          output: "ready\r\nnext\r\nlater\r\n",
        },
      },
    });
    await flushPromises();

    expect(terminal.write).toHaveBeenCalledTimes(2);
    wrapper.unmount();
  });

  it("does not duplicate delayed stream chunks already present in a newer snapshot", async () => {
    let streamListener: ((data: string) => void) | null = null;
    const wrapper = mount(TerminalSplitPane, {
      props: {
        node: leaf("snapshot-first"),
        sessions: {
          "snapshot-first": { id: "snapshot-first", output: "" },
        },
        subscribeOutput: (_sessionId, listener) => {
          streamListener = listener;
        },
      },
    });
    await wrapper.vm.$nextTick();

    const terminal = componentMocks.terminalInstances[0];
    await wrapper.setProps({
      sessions: {
        "snapshot-first": { id: "snapshot-first", output: "AB" },
      },
    });
    await flushPromises();
    expect(terminal.write).toHaveBeenLastCalledWith("AB");

    terminal.write.mockClear();
    const emitStream = streamListener as ((data: string) => void) | null;
    emitStream?.("A");
    emitStream?.("B");
    expect(terminal.write).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it("ignores queued output callbacks from a replaced terminal session", async () => {
    const listeners = new Map<string, (data: string) => void>();
    const wrapper = mount(TerminalSplitPane, {
      props: {
        node: leaf("old-session"),
        sessions: {
          "old-session": { id: "old-session", output: "old\r\n" },
          "new-session": { id: "new-session", output: "new\r\n" },
        },
        subscribeOutput: (sessionId, listener) => {
          listeners.set(sessionId, listener);
          return () => listeners.delete(sessionId);
        },
      },
    });
    await wrapper.vm.$nextTick();
    const oldListener = listeners.get("old-session");

    await wrapper.setProps({ node: leaf("new-session") });
    await flushPromises();
    const newTerminal = componentMocks.terminalInstances.at(-1);
    expect(newTerminal).toBeDefined();
    newTerminal?.write.mockClear();

    oldListener?.("stale\r\n");
    expect(newTerminal?.write).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it("hot-swaps same-session output subscribers and fences stale callbacks", async () => {
    let firstListener: ((data: string) => void) | null = null;
    let secondListener: ((data: string) => void) | null = null;
    const firstUnsubscribe = vi.fn();
    const secondUnsubscribe = vi.fn();
    const firstSubscriber = vi.fn(
      (_sessionId: string, listener: (data: string) => void) => {
        firstListener = listener;
        return firstUnsubscribe;
      },
    );
    const secondSubscriber = vi.fn(
      (_sessionId: string, listener: (data: string) => void) => {
        secondListener = listener;
        return secondUnsubscribe;
      },
    );
    const wrapper = mount(TerminalSplitPane, {
      props: {
        node: leaf("same-session"),
        sessions: {
          "same-session": { id: "same-session", output: "" },
        },
        subscribeOutput: firstSubscriber,
      },
    });
    await wrapper.vm.$nextTick();

    const terminal = componentMocks.terminalInstances[0];
    terminal.write.mockClear();
    const emitFirst = firstListener as ((data: string) => void) | null;
    emitFirst?.("first\r\n");
    expect(terminal.write).toHaveBeenLastCalledWith("first\r\n");

    await wrapper.setProps({ subscribeOutput: secondSubscriber });
    await flushPromises();
    expect(firstUnsubscribe).toHaveBeenCalledTimes(1);
    expect(secondSubscriber).toHaveBeenCalledWith(
      "same-session",
      expect.any(Function),
    );

    terminal.write.mockClear();
    emitFirst?.("stale\r\n");
    expect(terminal.write).not.toHaveBeenCalled();
    const emitSecond = secondListener as ((data: string) => void) | null;
    emitSecond?.("fresh\r\n");
    expect(terminal.write).toHaveBeenCalledTimes(1);
    expect(terminal.write).toHaveBeenLastCalledWith("fresh\r\n");

    wrapper.unmount();
    expect(secondUnsubscribe).toHaveBeenCalledTimes(1);
    terminal.write.mockClear();
    emitSecond?.("after-unmount\r\n");
    expect(terminal.write).not.toHaveBeenCalled();
  });

  it("isolates output subscription failures without breaking xterm cleanup", async () => {
    let firstListener: ((data: string) => void) | null = null;
    let replacementListener: ((data: string) => void) | null = null;
    const unsubscribeError = new Error("unsubscribe failed");
    const subscribeError = new Error("subscribe failed");
    const firstUnsubscribe = vi.fn(() => {
      throw unsubscribeError;
    });
    const replacementUnsubscribe = vi.fn();
    const firstSubscriber = vi.fn(
      (_sessionId: string, listener: (data: string) => void) => {
        firstListener = listener;
        return firstUnsubscribe;
      },
    );
    const replacementSubscriber = vi.fn(
      (_sessionId: string, listener: (data: string) => void) => {
        replacementListener = listener;
        return replacementUnsubscribe;
      },
    );
    const failingSubscriber = vi.fn(() => {
      throw subscribeError;
    });
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);
    const wrapper = mount(TerminalSplitPane, {
      props: {
        node: leaf("faulty-stream"),
        sessions: {
          "faulty-stream": { id: "faulty-stream", output: "" },
        },
        subscribeOutput: firstSubscriber,
      },
    });
    await wrapper.vm.$nextTick();

    const terminal = componentMocks.terminalInstances[0];
    await wrapper.setProps({ subscribeOutput: replacementSubscriber });
    await flushPromises();
    expect(firstUnsubscribe).toHaveBeenCalledTimes(1);
    expect(replacementSubscriber).toHaveBeenCalledWith(
      "faulty-stream",
      expect.any(Function),
    );

    terminal.write.mockClear();
    const emitFirst = firstListener as ((data: string) => void) | null;
    const emitReplacement = replacementListener as ((data: string) => void) | null;
    emitFirst?.("stale\r\n");
    emitReplacement?.("fresh\r\n");
    expect(terminal.write).toHaveBeenCalledTimes(1);
    expect(terminal.write).toHaveBeenLastCalledWith("fresh\r\n");

    await wrapper.setProps({ subscribeOutput: failingSubscriber });
    await flushPromises();
    expect(replacementUnsubscribe).toHaveBeenCalledTimes(1);
    expect(failingSubscriber).toHaveBeenCalledWith(
      "faulty-stream",
      expect.any(Function),
    );
    terminal.write.mockClear();
    emitReplacement?.("stale-after-failure\r\n");
    expect(terminal.write).not.toHaveBeenCalled();

    wrapper.unmount();
    expect(terminal.dispose).toHaveBeenCalledTimes(1);
    expect(consoleError).toHaveBeenCalledWith(
      "Failed to unsubscribe terminal split output:",
      unsubscribeError,
    );
    expect(consoleError).toHaveBeenCalledWith(
      "Failed to subscribe terminal split output:",
      subscribeError,
    );
  });

  it("disables, restores, and falls back to the default output subscriber", async () => {
    let voidListener: ((data: string) => void) | null = null;
    let restoredListener: ((data: string) => void) | null = null;
    const voidSubscriber = vi.fn(
      (_sessionId: string, listener: (data: string) => void) => {
        voidListener = listener;
      },
    );
    const restoredUnsubscribe = vi.fn();
    const restoredSubscriber = vi.fn(
      (_sessionId: string, listener: (data: string) => void) => {
        restoredListener = listener;
        return restoredUnsubscribe;
      },
    );
    const wrapper = mount(TerminalSplitPane, {
      props: {
        node: leaf("toggle-stream"),
        sessions: {
          "toggle-stream": { id: "toggle-stream", output: "" },
        },
        subscribeOutput: voidSubscriber,
      },
    });
    await wrapper.vm.$nextTick();

    const terminal = componentMocks.terminalInstances[0];
    const emitVoid = voidListener as ((data: string) => void) | null;
    await wrapper.setProps({ subscribeOutput: null });
    await flushPromises();
    terminal.write.mockClear();
    emitVoid?.("disabled-stale\r\n");
    expect(terminal.write).not.toHaveBeenCalled();

    await wrapper.setProps({ subscribeOutput: undefined });
    await flushPromises();
    expect(componentMocks.subscribeOutputMock).toHaveBeenCalledWith(
      "toggle-stream",
      expect.any(Function),
    );
    const defaultListener = componentMocks.streamListeners.get("toggle-stream");
    defaultListener?.("default\r\n");
    expect(terminal.write).toHaveBeenLastCalledWith("default\r\n");

    const defaultUnsubscribe = componentMocks.unsubscribeMocks.at(-1);
    await wrapper.setProps({ subscribeOutput: restoredSubscriber });
    await flushPromises();
    expect(defaultUnsubscribe).toHaveBeenCalledTimes(1);
    terminal.write.mockClear();
    defaultListener?.("default-stale\r\n");
    expect(terminal.write).not.toHaveBeenCalled();
    const emitRestored = restoredListener as ((data: string) => void) | null;
    emitRestored?.("restored\r\n");
    expect(terminal.write).toHaveBeenLastCalledWith("restored\r\n");

    wrapper.unmount();
    expect(restoredUnsubscribe).toHaveBeenCalledTimes(1);
  });

  it("resets the terminal when a session snapshot is cleared", async () => {
    const wrapper = mount(TerminalSplitPane, {
      props: {
        node: leaf("clear-output"),
        sessions: {
          "clear-output": { id: "clear-output", output: "ready\r\n" },
        },
        subscribeOutput: null,
      },
    });
    await wrapper.vm.$nextTick();

    const terminal = componentMocks.terminalInstances[0];
    terminal.write.mockClear();
    await wrapper.setProps({
      sessions: {
        "clear-output": { id: "clear-output", output: "" },
      },
    });
    await flushPromises();

    expect(terminal.reset).toHaveBeenCalledTimes(1);
    expect(terminal.write).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it("resets before rendering a non-prefix replacement snapshot", async () => {
    const wrapper = mount(TerminalSplitPane, {
      props: {
        node: leaf("replace-output"),
        sessions: {
          "replace-output": { id: "replace-output", output: "old\r\n" },
        },
        subscribeOutput: null,
      },
    });
    await wrapper.vm.$nextTick();

    const terminal = componentMocks.terminalInstances[0];
    terminal.write.mockClear();
    await wrapper.setProps({
      sessions: {
        "replace-output": {
          id: "replace-output",
          output: "replacement\r\n",
        },
      },
    });
    await flushPromises();

    expect(terminal.reset).toHaveBeenCalledTimes(1);
    expect(terminal.write).toHaveBeenCalledTimes(1);
    expect(terminal.write).toHaveBeenCalledWith("replacement\r\n");
    wrapper.unmount();
  });

  it("renders a recursive split and resizes it by keyboard and pointer", async () => {
    const node: SplitNode = {
      type: "split",
      id: "split:component-test",
      direction: "horizontal",
      children: [leaf("left"), leaf("right")],
      ratio: 0.5,
    };
    const wrapper = mount(TerminalSplitPane, {
      attachTo: document.body,
      props: {
        node,
        sessions: {
          left: { id: "left", output: "" },
          right: { id: "right", output: "" },
        },
      },
    });
    await wrapper.vm.$nextTick();

    expect(wrapper.findAll(".terminal-split-pane--leaf")).toHaveLength(2);
    expect(componentMocks.terminalInstances).toHaveLength(2);
    const separator = wrapper.get(".terminal-split-pane__separator");
    expect(separator.attributes("role")).toBe("separator");
    expect(separator.attributes("aria-orientation")).toBe("vertical");

    await separator.trigger("keydown", { key: "ArrowRight" });
    expect(wrapper.emitted("resize")?.at(-1)).toEqual([node, 0.55]);
    expect(wrapper.emitted("resize-end")?.at(-1)).toEqual([node, 0.55]);

    const splitElement = wrapper.get(
      ".terminal-split-pane--split",
    ).element;
    vi.spyOn(splitElement, "getBoundingClientRect").mockReturnValue({
      width: 1000,
      height: 600,
      top: 0,
      left: 0,
      right: 1000,
      bottom: 600,
      x: 0,
      y: 0,
      toJSON: () => ({}),
    });
    separator.element.dispatchEvent(
      new MouseEvent("pointerdown", {
        bubbles: true,
        button: 0,
        clientX: 500,
      }),
    );
    window.dispatchEvent(
      new MouseEvent("pointermove", { clientX: 600 }),
    );
    window.dispatchEvent(new MouseEvent("pointercancel"));

    const draggedRatio = wrapper.emitted("resize")?.at(-1)?.[1] as number;
    expect(draggedRatio).toBeCloseTo(0.65, 2);
    expect(wrapper.emitted("resize-end")?.at(-1)?.[0]).toStrictEqual(node);
    wrapper.unmount();
    expect(
      componentMocks.terminalInstances.every(
        (terminal) => terminal.dispose.mock.calls.length === 1,
      ),
    ).toBe(true);
  });

  it("reclamps and emits a resize when the minimum pane ratio increases", async () => {
    const node: SplitNode = {
      type: "split",
      id: "split:minimum-ratio",
      direction: "horizontal",
      children: [leaf("minimum-left"), leaf("minimum-right")],
      ratio: 0.05,
    };
    const wrapper = mount(TerminalSplitPane, {
      props: {
        node,
        minPaneRatio: 0.05,
        sessions: {
          "minimum-left": { id: "minimum-left", output: "" },
          "minimum-right": { id: "minimum-right", output: "" },
        },
      },
    });
    await wrapper.vm.$nextTick();

    await wrapper.setProps({ minPaneRatio: 0.25 });
    await flushPromises();

    const separator = wrapper.get('.terminal-split-pane__separator');
    expect(separator.attributes("aria-valuemin")).toBe("25");
    expect(separator.attributes("aria-valuenow")).toBe("25");
    expect(wrapper.emitted("resize")?.at(-1)).toEqual([node, 0.25]);
    wrapper.unmount();
  });

  it("uses injected terminal I/O handlers without requiring the store stream", async () => {
    const writeSession = vi.fn();
    const resizeTerminal = vi.fn();
    const wrapper = mount(TerminalSplitPane, {
      props: {
        node: leaf("standalone"),
        sessions: [{ id: "standalone", output: "" }],
        subscribeOutput: null,
        writeSession,
        resizeTerminal,
      },
    });
    await wrapper.vm.$nextTick();

    const terminal = componentMocks.terminalInstances[0];
    terminal.dataListener?.("echo independent\r");
    terminal.resizeListener?.({ cols: 90, rows: 20 });
    expect(writeSession).toHaveBeenCalledWith(
      "standalone",
      "echo independent\r",
    );
    expect(resizeTerminal).toHaveBeenCalledWith("standalone", 90, 20);
    expect(componentMocks.subscribeOutputMock).not.toHaveBeenCalled();
    wrapper.unmount();
  });
});

describe("MergeEditor conflict helpers", () => {
  const multiBlockConflict = [
    "before\n",
    "<<<<<<< HEAD\n",
    "ours-one\n",
    "=======\n",
    "theirs-one\n",
    ">>>>>>> feature-one\n",
    "middle\n",
    "<<<<<<< HEAD\n",
    "ours-two\n",
    "=======\n",
    "theirs-two\n",
    ">>>>>>> feature-two\n",
    "after\n",
  ].join("");

  it("applies the conflict-free three-way merge cases and marks a real conflict", () => {
    expect(createInitialThreeWayResult("base", "base", "theirs")).toBe(
      "theirs",
    );
    expect(createInitialThreeWayResult("base", "ours", "base")).toBe(
      "ours",
    );
    expect(
      createInitialThreeWayResult(
        "base",
        "ours",
        "theirs",
        "LOCAL",
        "REMOTE",
      ),
    ).toBe(
      "<<<<<<< LOCAL\nours\n=======\ntheirs\n>>>>>>> REMOTE",
    );

    expect(createInitialThreeWayResult(
      "one\ntwo\nthree\n",
      "ONE\ntwo\nthree\n",
      "one\ntwo\nTHREE\n",
    )).toBe("ONE\ntwo\nTHREE\n");

    expect(createInitialThreeWayResult(
      "one\ntwo\nthree\n",
      "one\nours\nthree\n",
      "one\ntheirs\nthree\n",
      "LOCAL",
      "REMOTE",
    )).toBe(
      "one\n<<<<<<< LOCAL\nours\n=======\ntheirs\n>>>>>>> REMOTE\nthree\n",
    );
  });

  it("parses multiple complete conflict blocks with exact offsets and lines", () => {
    const blocks = parseConflictBlocks(multiBlockConflict);

    expect(blocks).toHaveLength(2);
    expect(
      blocks.map(({ startLine, separatorLine, endLine }) => ({
        startLine,
        separatorLine,
        endLine,
      })),
    ).toEqual([
      { startLine: 2, separatorLine: 4, endLine: 6 },
      { startLine: 8, separatorLine: 10, endLine: 12 },
    ]);
    expect(blocks[0].ours).toBe("ours-one\n");
    expect(blocks[0].theirs).toBe("theirs-one\n");
    expect(
      multiBlockConflict.slice(blocks[1].startOffset, blocks[1].endOffset),
    ).toBe(blocks[1].raw);
  });

  it("parses custom marker sizes and blocks unresolved saves", () => {
    const content = [
      "<<<<<<<<<< ours\n",
      "left\n",
      "==========\n",
      "right\n",
      ">>>>>>>>>> theirs\n",
    ].join("");

    const [block] = parseConflictBlocks(content);
    expect(block).toEqual(expect.objectContaining({
      markerSize: 10,
      ours: "left\n",
      theirs: "right\n",
    }));
    expect(hasConflictMarkers(content)).toBe(true);
    expect(resolveConflictBlock(content, block, "theirs")).toBe("right\n");
  });

  it("excludes the diff3 base section from ours and theirs resolutions", () => {
    const content = [
      "<<<<<<< ours\n",
      "left\n",
      "||||||| base\n",
      "ancestor\n",
      "=======\n",
      "right\n",
      ">>>>>>> theirs\n",
    ].join("");

    const [block] = parseConflictBlocks(content);
    expect(block.ours).toBe("left\n");
    expect(block.baseLabel).toBe("base");
    expect(block.base).toBe("ancestor\n");
    expect(block.theirs).toBe("right\n");
    expect(resolveConflictBlock(content, block, "ours")).toBe("left\n");
    expect(resolveConflictBlock(content, block, "both")).toBe("left\nright\n");
  });

  it("does not invent a final newline when resolving an EOF conflict", () => {
    const content = createInitialThreeWayResult("base", "ours", "theirs");
    const [block] = parseConflictBlocks(content);
    const options = {
      oursEndsWithNewline: false,
      theirsEndsWithNewline: false,
    };

    expect(resolveConflictBlock(content, block, "ours", options)).toBe("ours");
    expect(resolveConflictBlock(content, block, "theirs", options)).toBe("theirs");
    expect(resolveConflictBlock(content, block, "both", options)).toBe("ours\ntheirs");
  });

  it.each([
    {
      name: "invalid line numbers",
      content: "only\n",
      regions: [
        { startLine: 0, endLine: 1, oursLines: [], theirsLines: [], baseLines: [] },
      ],
    },
    {
      name: "an out-of-range end line",
      content: "only\n",
      regions: [
        { startLine: 1, endLine: 2, oursLines: [], theirsLines: [], baseLines: [] },
      ],
    },
    {
      name: "overlapping regions",
      content: "one\ntwo\n",
      regions: [
        { startLine: 1, endLine: 2, oursLines: [], theirsLines: [], baseLines: [] },
        { startLine: 2, endLine: 2, oursLines: [], theirsLines: [], baseLines: [] },
      ],
    },
    {
      name: "duplicate EOF insertions",
      content: "only\n",
      regions: [
        { startLine: 2, endLine: 2, oursLines: [], theirsLines: [], baseLines: [] },
        { startLine: 2, endLine: 2, oursLines: [], theirsLines: [], baseLines: [] },
      ],
    },
  ])("rejects $name", ({ content, regions }) => {
    expect(() => createConflictRegionResult(content, regions)).toThrow();
  });

  it("keeps malformed conflict markers unresolved", () => {
    const malformed = "<<<<<<< HEAD\nours\n";
    expect(parseConflictBlocks(malformed)).toHaveLength(0);
    expect(hasConflictMarkers(malformed)).toBe(true);
    expect(hasConflictMarkers("resolved\ncontent\n")).toBe(false);
    expect(hasConflictMarkers("heading\n=======\ncontent\n")).toBe(false);
  });

  it("resolves only the selected block as ours, theirs, or both", () => {
    const blocks = parseConflictBlocks(multiBlockConflict);
    const ours = resolveConflictBlock(multiBlockConflict, blocks[0], "ours");
    const theirs = resolveConflictBlock(
      multiBlockConflict,
      blocks[1],
      "theirs",
    );
    const both = resolveConflictBlock(multiBlockConflict, blocks[0], "both");

    expect(ours).toContain("before\nours-one\nmiddle\n<<<<<<< HEAD\n");
    expect(ours).not.toContain("theirs-one");
    expect(theirs).toContain(">>>>>>> feature-one\nmiddle\ntheirs-two\nafter\n");
    expect(theirs).not.toContain("ours-two");
    expect(both).toContain("before\nours-one\ntheirs-one\nmiddle\n");
    expect(parseConflictBlocks(ours)).toHaveLength(1);
    expect(parseConflictBlocks(theirs)).toHaveLength(1);
    expect(parseConflictBlocks(both)).toHaveLength(1);
  });

  it("stabilizes block ids after leading text and one side are edited", () => {
    const previous = parseConflictBlocks(multiBlockConflict);
    const edited = `inserted\n${multiBlockConflict.replace(
      "ours-one",
      "ours-one-edited",
    )}`;
    const stabilized = stabilizeConflictBlockIds(
      parseConflictBlocks(edited),
      previous,
    );

    expect(stabilized.map((block) => block.id)).toEqual(
      previous.map((block) => block.id),
    );
    expect(new Set(stabilized.map((block) => block.id)).size).toBe(2);
  });

  it("preserves CRLF content and joins repository-relative paths", () => {
    const content = createInitialThreeWayResult(
      "base\r\n",
      "ours\r\n",
      "theirs\r\n",
      "LOCAL",
      "REMOTE",
    );
    const block = parseConflictBlocks(content)[0];

    expect(content).toBe(
      "<<<<<<< LOCAL\r\nours\r\n=======\r\ntheirs\r\n>>>>>>> REMOTE",
    );
    expect(block.ours).toBe("ours\r\n");
    expect(block.theirs).toBe("theirs\r\n");
    expect(resolveConflictBlock(content, block, "both")).toBe(
      "ours\r\ntheirs\r\n",
    );
    expect(joinRepoFilePath("C:\\repo\\", "src/foo.ts")).toBe(
      "C:\\repo\\src\\foo.ts",
    );
    expect(joinRepoFilePath("/repo/", "src\\foo.ts")).toBe(
      "/repo/src/foo.ts",
    );
    expect(joinRepoFilePath("", "src/file.ts")).toBe("src/file.ts");

    expect(createInitialThreeWayResult(
      "first\r\nlast",
      "first\r\nours",
      "first\r\ntheirs",
    )).toBe(
      "first\r\n<<<<<<< OURS\r\nours\r\n=======\r\ntheirs\r\n>>>>>>> THEIRS",
    );
  });

  it.each([
    "../outside.ts",
    "src/../../outside.ts",
    "..\\outside.ts",
    "/absolute/outside.ts",
    "\\rooted\\outside.ts",
    "C:\\outside.ts",
    "C:outside.ts",
    "\\\\server\\share\\outside.ts",
    "//server/share/outside.ts",
  ])("rejects unsafe repository file path %s", (filePath) => {
    expect(() => joinRepoFilePath("C:\\repo", filePath)).toThrow();
  });
});

describe("MergeEditor component", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("creates three diff editors, saves a resolved result, and disposes resources", async () => {
    resetMonacoMocks();
    const saveResult = vi.fn().mockResolvedValue(undefined);
    const initialResult = createInitialThreeWayResult(
      "base\n",
      "ours\n",
      "theirs\n",
    );
    const wrapper = mount(MergeEditor, {
      props: {
        repoPath: "C:\\repo",
        filePath: "src/file.ts",
        base: "base\n",
        ours: "ours\n",
        theirs: "theirs\n",
        saveAdapter: { saveResult },
      },
      global: {
        stubs: {
          "el-button": true,
          "el-icon": true,
        },
      },
    });
    await flushPromises();

    expect(monacoMocks.createDiffEditor).toHaveBeenCalledTimes(3);
    expect(
      monacoMocks.createDiffEditor.mock.calls.map(([, options]) => ({
        originalAriaLabel: options.originalAriaLabel,
        modifiedAriaLabel: options.modifiedAriaLabel,
      })),
    ).toEqual([
      {
        originalAriaLabel: "mergeEditor.oursBaseAria",
        modifiedAriaLabel: "mergeEditor.ours",
      },
      {
        originalAriaLabel: "mergeEditor.resultBaseAria",
        modifiedAriaLabel: "mergeEditor.result",
      },
      {
        originalAriaLabel: "mergeEditor.theirsBaseAria",
        modifiedAriaLabel: "mergeEditor.theirs",
      },
    ]);
    expect(monacoMocks.createModel).toHaveBeenCalledTimes(6);
    expect(monacoMocks.models[1].getValue()).toBe("ours\n");
    expect(monacoMocks.models[3].getValue()).toBe(initialResult);
    expect(monacoMocks.models[5].getValue()).toBe("theirs\n");
    expect(monacoMocks.models[3].onDidChangeContent).toHaveBeenCalledTimes(1);

    const exposed = wrapper.vm as unknown as {
      acceptConflict: (
        block: ReturnType<typeof parseConflictBlocks>[number],
        resolution: "ours" | "theirs" | "both",
      ) => void;
      getResult: () => string;
      saveResult: () => Promise<void>;
    };
    exposed.acceptConflict(parseConflictBlocks(initialResult)[0], "ours");
    await flushPromises();
    expect(exposed.getResult()).toBe("ours\n");
    expect(parseConflictBlocks(exposed.getResult())).toHaveLength(0);

    await exposed.saveResult();
    expect(saveResult).toHaveBeenCalledWith({
      repoPath: "C:\\repo",
      filePath: "src/file.ts",
      path: "C:\\repo\\src\\file.ts",
      content: "ours\n",
    });
    expect(wrapper.emitted("saved")).toEqual([
      [
        {
          repoPath: "C:\\repo",
          filePath: "src/file.ts",
          path: "C:\\repo\\src\\file.ts",
          content: "ours\n",
        },
      ],
    ]);

    const resultModel = monacoMocks.models[3];
    wrapper.unmount();
    expect(resultModel.listenerDispose).toHaveBeenCalledTimes(1);
    for (const editor of monacoMocks.editors) {
      expect(editor.setModel).toHaveBeenLastCalledWith(null);
      expect(editor.dispose).toHaveBeenCalledTimes(1);
    }
    for (const model of monacoMocks.models) {
      expect(model.dispose).toHaveBeenCalledTimes(1);
    }
  });

  it("writes and stages the same repository when using the default services", async () => {
    resetMonacoMocks();
    const { fileService, gitService } = await import("@/api/services");
    const writeFile = vi.spyOn(fileService, "writeFile").mockResolvedValue(undefined);
    const stage = vi.spyOn(gitService, "stage").mockResolvedValue(undefined);
    const resolveConflict = vi.spyOn(gitService, "resolveConflict").mockResolvedValue(undefined);
    const wrapper = mount(MergeEditor, {
      props: {
        repoPath: "D:\\linked-worktree",
        filePath: "src/file.ts",
        base: "base\n",
        ours: "resolved\n",
        theirs: "resolved\n",
      },
      global: {
        stubs: {
          "el-button": true,
          "el-icon": true,
        },
      },
    });
    await flushPromises();

    const exposed = wrapper.vm as unknown as { saveResult: () => Promise<void> };
    await exposed.saveResult();

    expect(writeFile).toHaveBeenCalledWith(
      "D:\\linked-worktree\\src\\file.ts",
      "resolved\n",
    );
    expect(stage).toHaveBeenCalledWith("D:\\linked-worktree", "src/file.ts");
    expect(resolveConflict).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it("rejects an unsafe legacy save path before default services write or stage", async () => {
    resetMonacoMocks();
    const { fileService, gitService } = await import("@/api/services");
    const writeFile = vi.spyOn(fileService, "writeFile").mockResolvedValue(undefined);
    const stage = vi.spyOn(gitService, "stage").mockResolvedValue(undefined);
    const wrapper = mount(MergeEditor, {
      props: {
        repoPath: "C:\\repo",
        filePath: "..\\outside.ts",
        base: "same",
        ours: "resolved",
        theirs: "resolved",
      },
      global: {
        stubs: {
          "el-button": true,
          "el-icon": true,
        },
      },
    });
    await flushPromises();

    const exposed = wrapper.vm as unknown as { saveResult: () => Promise<void> };
    await exposed.saveResult();

    expect(writeFile).not.toHaveBeenCalled();
    expect(stage).not.toHaveBeenCalled();
    expect(wrapper.emitted("save")).toBeUndefined();
    expect(wrapper.emitted("saved")).toBeUndefined();
    expect(wrapper.emitted("error")).toHaveLength(1);
    wrapper.unmount();
  });

  it.each(["ours", "theirs", "both"] as const)(
    "preserves a missing final newline when accepting %s",
    async (resolution) => {
      resetMonacoMocks();
      const wrapper = mount(MergeEditor, {
        props: {
          repoPath: "/repo",
          filePath: "src/file.ts",
          base: "base",
          ours: "ours",
          theirs: "theirs",
        },
        global: {
          stubs: {
            "el-button": true,
            "el-icon": true,
          },
        },
      });
      await flushPromises();

      const exposed = wrapper.vm as unknown as {
        acceptConflict: (
          block: ReturnType<typeof parseConflictBlocks>[number],
          resolution: "ours" | "theirs" | "both",
        ) => void;
        getResult: () => string;
      };
      const initial = exposed.getResult();
      exposed.acceptConflict(parseConflictBlocks(initial)[0], resolution);
      await flushPromises();

      expect(exposed.getResult()).toBe(
        resolution === "ours"
          ? "ours"
          : resolution === "theirs"
            ? "theirs"
            : "ours\ntheirs",
      );
      wrapper.unmount();
    },
  );

  it("preserves missing final newlines for a pre-parsed EOF region", async () => {
    const conflictRegions = [{
      startLine: 1,
      endLine: 1,
      oursLines: ["ours"],
      theirsLines: ["theirs"],
      baseLines: ["base"],
    }];

    for (const resolution of ["ours", "theirs", "both"] as const) {
      resetMonacoMocks();
      const wrapper = mount(MergeEditor, {
        props: {
          repoPath: "/repo",
          filePath: "src/file.ts",
          base: "base",
          ours: "ours",
          theirs: "theirs",
          conflictRegions,
        },
        global: {
          stubs: {
            "el-button": true,
            "el-icon": true,
          },
        },
      });
      await flushPromises();

      const exposed = wrapper.vm as unknown as {
        acceptConflict: (
          block: ReturnType<typeof parseConflictBlocks>[number],
          resolution: "ours" | "theirs" | "both",
        ) => void;
        getResult: () => string;
      };
      const initial = exposed.getResult();
      exposed.acceptConflict(parseConflictBlocks(initial)[0], resolution);
      await flushPromises();

      expect(exposed.getResult()).toBe(
        resolution === "ours"
          ? "ours"
          : resolution === "theirs"
            ? "theirs"
            : "ours\ntheirs",
      );
      wrapper.unmount();
    }
  });

  it("keeps saving disabled when pre-parsed conflict regions are invalid", async () => {
    resetMonacoMocks();
    const saveResult = vi.fn().mockResolvedValue(undefined);
    const wrapper = mount(MergeEditor, {
      props: {
        repoPath: "/repo",
        filePath: "src/file.ts",
        base: "base",
        ours: "ours",
        theirs: "theirs",
        conflictRegions: [{
          startLine: 1,
          endLine: 2,
          oursLines: ["ours"],
          theirsLines: ["theirs"],
          baseLines: ["base"],
        }],
        saveAdapter: { saveResult },
      },
      global: {
        stubs: {
          "el-button": true,
          "el-icon": true,
        },
      },
    });
    await flushPromises();

    const exposed = wrapper.vm as unknown as { saveResult: () => Promise<void> };
    await exposed.saveResult();

    expect(saveResult).not.toHaveBeenCalled();
    expect(wrapper.emitted("error")).toHaveLength(1);
    wrapper.unmount();
  });

  it("does not reset manual edits for an equivalent conflict-region array", async () => {
    resetMonacoMocks();
    const conflictRegions = [{
      startLine: 1,
      endLine: 1,
      oursLines: ["ours"],
      theirsLines: ["theirs"],
      baseLines: ["base"],
    }];
    const wrapper = mount(MergeEditor, {
      props: {
        repoPath: "/repo",
        filePath: "src/file.ts",
        base: "base",
        ours: "ours",
        theirs: "theirs",
        conflictRegions,
      },
      global: {
        stubs: {
          "el-button": true,
          "el-icon": true,
        },
      },
    });
    await flushPromises();

    const setResultModelValue = monacoMocks.models[3].setValue as unknown as
      (value: string) => void;
    setResultModelValue("manual result");
    await wrapper.setProps({
      conflictRegions: conflictRegions.map((region) => ({
        ...region,
        oursLines: [...region.oursLines],
        theirsLines: [...region.theirsLines],
        baseLines: [...region.baseLines],
      })),
    });
    await flushPromises();

    expect(monacoMocks.models[3].getValue()).toBe("manual result");

    await wrapper.setProps({
      conflictRegions: [{
        ...conflictRegions[0],
        theirsLines: ["updated-theirs"],
      }],
    });
    await flushPromises();

    expect(monacoMocks.models[3].getValue()).toContain("updated-theirs");
    wrapper.unmount();
  });

  it("does not save a result containing malformed conflict markers", async () => {
    resetMonacoMocks();
    const saveResult = vi.fn().mockResolvedValue(undefined);
    const wrapper = mount(MergeEditor, {
      props: {
        repoPath: "/repo",
        filePath: "src/file.ts",
        base: "base\n",
        ours: "ours\n",
        theirs: "theirs\n",
        initialResult: "<<<<<<< HEAD\nours\n",
        saveAdapter: { saveResult },
      },
      global: {
        stubs: {
          "el-button": true,
          "el-icon": true,
        },
      },
    });
    await flushPromises();

    const exposed = wrapper.vm as unknown as { saveResult: () => Promise<void> };
    await exposed.saveResult();
    expect(saveResult).not.toHaveBeenCalled();
    wrapper.unmount();
  });
});

interface WorktreeFixture {
  path: string;
  head: string;
  branch: string;
  bare: boolean;
  locked?: string;
  prunable?: boolean;
}

function createWorktreeService(worktrees: WorktreeFixture[]) {
  return {
    ListWorktrees: vi.fn().mockResolvedValue(worktrees),
    AddWorktree: vi.fn().mockResolvedValue(undefined),
    RemoveWorktree: vi.fn().mockResolvedValue(undefined),
    PruneWorktrees: vi.fn().mockResolvedValue(undefined),
    LockWorktree: vi.fn().mockResolvedValue(undefined),
    UnlockWorktree: vi.fn().mockResolvedValue(undefined),
  };
}

function deferred<T>() {
  let resolve: (value: T | PromiseLike<T>) => void = () => undefined;
  let reject: (reason?: unknown) => void = () => undefined;
  const promise = new Promise<T>((promiseResolve, promiseReject) => {
    resolve = promiseResolve;
    reject = promiseReject;
  });
  return { promise, resolve, reject };
}

describe("WorktreePanel", () => {
  const worktrees: WorktreeFixture[] = [
    {
      path: "C:\\repo-main",
      head: "1234567890abcdef",
      branch: "main",
      bare: false,
    },
    {
      path: "C:\\repo-release",
      head: "abcdef1234567890",
      branch: "release",
      bare: false,
      locked: "release validation",
      prunable: true,
    },
  ];

  beforeEach(() => {
    integrationMocks.messageSuccess.mockReset();
    integrationMocks.messageError.mockReset();
    integrationMocks.messageBoxConfirm.mockReset().mockResolvedValue("confirm");
    integrationMocks.messageBoxPrompt
      .mockReset()
      .mockResolvedValue({ value: "maintenance" });
  });

  it("shows a load error and retries the worktree list successfully", async () => {
    const service = createWorktreeService(worktrees);
    service.ListWorktrees
      .mockRejectedValueOnce(new Error("worktree service offline"))
      .mockResolvedValueOnce(worktrees);
    const wrapper = mount(WorktreePanel, {
      props: {
        repoPath: "C:\\repo",
        branches: ["main"],
        service,
        revealService: null,
      },
    });
    await flushPromises();

    expect(wrapper.get('[role="alert"]').text()).toContain(
      "worktree.loadFailed",
    );
    expect(wrapper.findAll(".worktree-panel__row")).toHaveLength(0);

    await wrapper
      .get('[aria-label="worktree.retryAria"]')
      .trigger("click");
    await flushPromises();

    expect(service.ListWorktrees).toHaveBeenCalledTimes(2);
    expect(wrapper.find('[role="alert"]').exists()).toBe(false);
    expect(wrapper.findAll(".worktree-panel__row")).toHaveLength(2);
    wrapper.unmount();
  });

  it("renders injected worktrees and submits all add options", async () => {
    const service = createWorktreeService(worktrees);
    const revealService = { revealInOS: vi.fn().mockResolvedValue(undefined) };
    const wrapper = mount(WorktreePanel, {
      props: {
        repoPath: "C:\\repo",
        branches: ["main", "develop"],
        service,
        revealService,
      },
    });
    await flushPromises();

    expect(wrapper.findAll(".worktree-panel__row")).toHaveLength(2);
    expect(wrapper.text()).toContain("C:\\repo-main");
    expect(wrapper.text()).toContain("12345678");
    expect(wrapper.text()).toContain("worktree.locked");
    expect(wrapper.text()).toContain("worktree.prunable");

    await wrapper.get('[aria-label="worktree.addAria"]').trigger("click");
    await wrapper
      .get('input[type="radio"][value="new"]')
      .setValue(true);
    await wrapper
      .get('[aria-label="worktree.pathAria"]')
      .setValue("C:\\repo-feature");
    await wrapper
      .get('[aria-label="worktree.newBranchAria"]')
      .setValue("feature/test");
    await wrapper
      .get('[aria-label="worktree.startPointAria"]')
      .setValue("develop");
    await wrapper.findAll(".worktree-panel__options input")[1].setValue(true);
    await wrapper.get(".worktree-panel__form").trigger("submit");
    await flushPromises();

    expect(service.AddWorktree).toHaveBeenCalledWith(
      "C:\\repo",
      "C:\\repo-feature",
      "develop",
      {
        newBranch: "feature/test",
        detach: false,
        force: true,
        allowOutsideRepository: true,
      },
    );
    expect(service.ListWorktrees).toHaveBeenCalledTimes(2);
    expect(integrationMocks.messageSuccess).toHaveBeenCalledWith(
      "worktree.addSuccess",
    );
    wrapper.unmount();
  });

  it.each([
    ["resolves", false],
    ["rejects", true],
  ] as const)(
    "preserves a new add operation when an old repository add %s",
    async (_outcome, rejectOldAdd) => {
      const oldService = createWorktreeService(worktrees);
      const newService = createWorktreeService(worktrees);
      const oldAdd = deferred<void>();
      const newAdd = deferred<void>();
      oldService.AddWorktree.mockReturnValueOnce(oldAdd.promise);
      newService.AddWorktree.mockReturnValueOnce(newAdd.promise);
      const wrapper = mount(WorktreePanel, {
        props: {
          repoPath: "C:\\repo-old",
          branches: ["main"],
          service: oldService,
          revealService: null,
        },
      });
      await flushPromises();

      await wrapper.get('[aria-label="worktree.addAria"]').trigger("click");
      await wrapper
        .get('[aria-label="worktree.pathAria"]')
        .setValue("C:\\repo-old-feature");
      await wrapper.get(".worktree-panel__form").trigger("submit");
      expect(oldService.AddWorktree).toHaveBeenCalledTimes(1);

      await wrapper.setProps({
        repoPath: "C:\\repo-new",
        service: newService,
      });
      await flushPromises();
      await wrapper.get('[aria-label="worktree.addAria"]').trigger("click");
      await wrapper
        .get('[aria-label="worktree.pathAria"]')
        .setValue("C:\\repo-new-feature");
      await wrapper.get(".worktree-panel__form").trigger("submit");
      expect(newService.AddWorktree).toHaveBeenCalledWith(
        "C:\\repo-new",
        "C:\\repo-new-feature",
        "main",
        {
          newBranch: "",
          detach: false,
          force: false,
          allowOutsideRepository: true,
        },
      );
      expect(
        wrapper.get('[aria-label="worktree.submitAddAria"]').attributes("aria-busy"),
      ).toBe("true");
      const oldListCallsBeforeSettlement = oldService.ListWorktrees.mock.calls.length;
      const newListCallsBeforeSettlement = newService.ListWorktrees.mock.calls.length;

      if (rejectOldAdd) oldAdd.reject(new Error("old add failed"));
      else oldAdd.resolve(undefined);
      await flushPromises();

      expect(wrapper.find(".worktree-panel__form").exists()).toBe(true);
      expect(
        wrapper.get<HTMLInputElement>('[aria-label="worktree.pathAria"]').element.value,
      ).toBe("C:\\repo-new-feature");
      expect(
        wrapper.get('[aria-label="worktree.submitAddAria"]').attributes("aria-busy"),
      ).toBe("true");
      expect(integrationMocks.messageSuccess).not.toHaveBeenCalled();
      expect(integrationMocks.messageError).not.toHaveBeenCalled();
      expect(oldService.ListWorktrees).toHaveBeenCalledTimes(
        oldListCallsBeforeSettlement,
      );
      expect(newService.ListWorktrees).toHaveBeenCalledTimes(
        newListCallsBeforeSettlement,
      );

      newAdd.resolve(undefined);
      await flushPromises();
      expect(wrapper.find(".worktree-panel__form").exists()).toBe(false);
      expect(integrationMocks.messageSuccess).toHaveBeenCalledWith(
        "worktree.addSuccess",
      );
      expect(newService.ListWorktrees).toHaveBeenCalledTimes(
        newListCallsBeforeSettlement + 1,
      );
      wrapper.unmount();
    },
  );

  it("opens a worktree and confirms its removal", async () => {
    const service = createWorktreeService(worktrees);
    const revealService = { revealInOS: vi.fn().mockResolvedValue(undefined) };
    const wrapper = mount(WorktreePanel, {
      props: {
        repoPath: "C:\\repo",
        branches: ["main"],
        service,
        revealService,
      },
    });
    await flushPromises();

    await wrapper
      .findAll('[aria-label="worktree.openAria"]')[0]
      .trigger("click");
    await flushPromises();
    expect(revealService.revealInOS).toHaveBeenCalledWith("C:\\repo-main");
    expect(wrapper.emitted("open")).toEqual([["C:\\repo-main"]]);

    await wrapper
      .findAll('[aria-label="worktree.removeAria"]')[1]
      .trigger("click");
    await flushPromises();
    expect(integrationMocks.messageBoxConfirm).toHaveBeenCalledWith(
      "worktree.removeConfirm",
      "worktree.removeTitle",
      expect.objectContaining({ type: "warning" }),
    );
    expect(service.RemoveWorktree).toHaveBeenCalledWith(
      "C:\\repo",
      "C:\\repo-release",
      false,
    );
    wrapper.unmount();
  });

  it("offers a second confirmation and retries dirty removal with force", async () => {
    const service = createWorktreeService(worktrees);
    service.RemoveWorktree
      .mockRejectedValueOnce(new Error("worktree is dirty; use --force"))
      .mockResolvedValueOnce(undefined);
    const wrapper = mount(WorktreePanel, {
      props: {
        repoPath: "C:\\repo",
        branches: ["main"],
        service,
        revealService: null,
      },
    });
    await flushPromises();

    await wrapper
      .findAll('[aria-label="worktree.removeAria"]')[1]
      .trigger("click");
    await flushPromises();

    expect(integrationMocks.messageBoxConfirm).toHaveBeenCalledTimes(2);
    expect(integrationMocks.messageBoxConfirm).toHaveBeenNthCalledWith(
      2,
      "worktree.forceRemoveConfirm",
      "worktree.forceRemoveTitle",
      expect.objectContaining({
        confirmButtonText: "worktree.forceRemoveAction",
        type: "warning",
      }),
    );
    expect(service.RemoveWorktree.mock.calls).toEqual([
      ["C:\\repo", "C:\\repo-release", false],
      ["C:\\repo", "C:\\repo-release", true],
    ]);
    expect(integrationMocks.messageSuccess).toHaveBeenCalledWith(
      "worktree.removeSuccess",
    );
    wrapper.unmount();
  });

  it("abandons a confirmed removal when the repository changes", async () => {
    const service = createWorktreeService(worktrees);
    const confirmation = deferred<unknown>();
    integrationMocks.messageBoxConfirm.mockReturnValueOnce(
      confirmation.promise,
    );
    const wrapper = mount(WorktreePanel, {
      props: {
        repoPath: "C:\\repo",
        branches: ["main"],
        service,
        revealService: null,
      },
    });
    await flushPromises();

    await wrapper
      .findAll('[aria-label="worktree.removeAria"]')[1]
      .trigger("click");
    expect(integrationMocks.messageBoxConfirm).toHaveBeenCalledTimes(1);

    await wrapper.setProps({ repoPath: "C:\\repo-next" });
    await flushPromises();
    confirmation.resolve("confirm");
    await flushPromises();

    expect(service.ListWorktrees).toHaveBeenCalledWith("C:\\repo");
    expect(service.ListWorktrees).toHaveBeenCalledWith("C:\\repo-next");
    expect(service.RemoveWorktree).not.toHaveBeenCalled();
    expect(integrationMocks.messageSuccess).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it("locks, unlocks, and confirms pruning through the injected service", async () => {
    const service = createWorktreeService([
      worktrees[0],
      {
        path: "C:\\repo-feature",
        head: "fedcba0987654321",
        branch: "feature",
        bare: false,
      },
      worktrees[1],
    ]);
    const wrapper = mount(WorktreePanel, {
      props: {
        repoPath: "C:\\repo",
        branches: ["main"],
        service,
        revealService: null,
      },
    });
    await flushPromises();

    await wrapper
      .findAll('[aria-label="worktree.lockAria"]')[1]
      .trigger("click");
    await flushPromises();
    expect(integrationMocks.messageBoxPrompt).toHaveBeenCalled();
    expect(service.LockWorktree).toHaveBeenCalledWith(
      "C:\\repo",
      "C:\\repo-feature",
      "maintenance",
    );

    await wrapper
      .findAll('[aria-label="worktree.unlockAria"]')[0]
      .trigger("click");
    await flushPromises();
    expect(service.UnlockWorktree).toHaveBeenCalledWith(
      "C:\\repo",
      "C:\\repo-release",
    );

    await wrapper
      .get('[aria-label="worktree.pruneAria"]')
      .trigger("click");
    await flushPromises();
    expect(integrationMocks.messageBoxConfirm).toHaveBeenCalledWith(
      "worktree.pruneConfirm",
      "worktree.pruneTitle",
      expect.objectContaining({ type: "warning" }),
    );
    expect(service.PruneWorktrees).toHaveBeenCalledWith("C:\\repo", false);
    wrapper.unmount();
  });
});

describe("featureRegistry", () => {
  const builtInIds = [
    "debug.threads",
    "terminal.split",
    "git.worktree",
    "git.merge-editor",
    "git.rebase-editor",
  ];
  const customFeatureIds = new Set<string>();

  function registerCustomFeature(
    feature: Parameters<typeof registerFeature>[0],
  ): void {
    registerFeature(feature);
    customFeatureIds.add(feature.id.trim());
  }

  beforeEach(() => {
    integrationMocks.runtimeEventsOn.mockImplementation(() => vi.fn());
    for (const id of builtInIds) activateFeature(id);
  });

  afterEach(() => {
    for (const id of customFeatureIds) {
      if (isFeatureActive(id)) deactivateFeature(id);
      unregisterFeature(id);
    }
    customFeatureIds.clear();
    integrationMocks.runtimeEventsOn.mockImplementation(() => vi.fn());
    for (const id of builtInIds) activateFeature(id);
  });

  it("registers and activates all built-in features with component loaders", () => {
    expect(listFeatures().map((feature) => feature.id)).toEqual(builtInIds);
    for (const id of builtInIds) {
      expect(isFeatureActive(id)).toBe(true);
      expect(getFeatureComponentLoader(id)).toEqual(expect.any(Function));
    }
  });

  it("fully deactivates and reactivates built-ins for HMR cleanup", () => {
    deactivateAllFeatures();
    expect(isDebugThreadsActive()).toBe(false);
    for (const id of builtInIds) {
      expect(isFeatureActive(id)).toBe(false);
      expect(getFeatureComponentLoader(id)).toBeUndefined();
    }

    activateAllFeatures();
    expect(isDebugThreadsActive()).toBe(true);
    for (const id of builtInIds) {
      expect(isFeatureActive(id)).toBe(true);
      expect(getFeatureComponentLoader(id)).toEqual(expect.any(Function));
    }
  });

  it("returns defensive feature registration snapshots", () => {
    const snapshot = listFeatures();
    snapshot[0].id = "mutated.feature";
    snapshot[0].name = "Mutated feature";

    expect(listFeatures().map((feature) => feature.id)).toEqual(builtInIds);
    expect(listFeatures()[0].name).toBe("Multi-thread Debugging");
  });

  it("keeps activate and deactivate operations idempotent", () => {
    const id = "terminal.split";

    expect(deactivateFeature(id)).toBe(true);
    expect(deactivateFeature(id)).toBe(true);
    expect(isFeatureActive(id)).toBe(false);
    expect(getFeatureComponentLoader(id)).toBeUndefined();

    expect(activateFeature(id)).toBe(true);
    const loader = getFeatureComponentLoader(id);
    expect(loader).toEqual(expect.any(Function));
    expect(activateFeature(id)).toBe(true);
    expect(getFeatureComponentLoader(id)).toBe(loader);
  });

  it("ignores duplicate feature registrations without changing the registry", () => {
    const consoleWarn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    registerFeature({
      id: "terminal.split",
      name: "Duplicate terminal split",
      activate: vi.fn(),
    });
    expect(listFeatures().map((feature) => feature.id)).toEqual(builtInIds);
    expect(consoleWarn).toHaveBeenCalledWith(
      '[featureRegistry] feature "terminal.split" already registered',
    );
    consoleWarn.mockRestore();
  });

  it("validates feature ids, names, and lifecycle callbacks", () => {
    expect(() =>
      registerFeature({
        id: "invalid feature",
        name: "Invalid id",
        activate: vi.fn(),
      }),
    ).toThrow("Invalid feature id: invalid feature");
    expect(() =>
      registerFeature({
        id: "custom.empty-name",
        name: "   ",
        activate: vi.fn(),
      }),
    ).toThrow("Feature custom.empty-name must have a name");
    expect(() =>
      registerFeature({
        id: "custom.invalid-activate",
        name: "Invalid activate",
        activate: null as unknown as () => void,
      }),
    ).toThrow("Feature custom.invalid-activate must provide an activate function");
    expect(() =>
      registerFeature({
        id: "custom.invalid-deactivate",
        name: "Invalid deactivate",
        activate: vi.fn(),
        deactivate: "invalid" as unknown as () => void,
      }),
    ).toThrow("Feature custom.invalid-deactivate has an invalid deactivate function");
  });

  it("runs a custom lifecycle, loads its component, and protects active unregister", async () => {
    const id = "custom.lifecycle";
    const activate = vi.fn();
    const deactivate = vi.fn();
    const componentModule = { default: { name: "CustomFeature" } };
    const loader = vi.fn().mockResolvedValue(componentModule);
    registerCustomFeature({
      id,
      name: "Custom lifecycle",
      activate,
      deactivate,
    });

    expect(getFeatureActivationError(id)).toBeUndefined();
    setFeatureComponentLoader(id, loader);
    await expect(loadFeatureComponent(id)).resolves.toBe(componentModule);
    expect(loader).toHaveBeenCalledTimes(1);
    expect(activateFeature(id)).toBe(true);
    expect(activate).toHaveBeenCalledTimes(1);
    expect(isFeatureActive(id)).toBe(true);
    expect(unregisterFeature(id)).toBe(false);

    expect(deactivateFeature(id)).toBe(true);
    expect(deactivate).toHaveBeenCalledTimes(1);
    expect(isFeatureActive(id)).toBe(false);
    expect(unregisterFeature(` ${id} `)).toBe(true);
    customFeatureIds.delete(id);

    expect(() => setFeatureComponentLoader("custom.missing", loader)).toThrow(
      "Feature custom.missing is not registered",
    );
    await expect(loadFeatureComponent("custom.missing")).rejects.toThrow(
      "Feature custom.missing has no registered component",
    );
    expect(activateFeature("custom.missing")).toBe(false);
    expect(deactivateFeature("custom.missing")).toBe(false);
  });

  it("records activation and deactivation failures without leaking custom features", () => {
    const activationId = "custom.activation-error";
    const activationError = new Error("activation failed");
    registerCustomFeature({
      id: activationId,
      name: "Activation error",
      activate: () => {
        throw activationError;
      },
    });

    const consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);
    try {
      expect(activateFeature(activationId)).toBe(false);
      expect(isFeatureActive(activationId)).toBe(false);
      expect(getFeatureActivationError(activationId)).toBe(activationError);
      expect(unregisterFeature(activationId)).toBe(true);
      customFeatureIds.delete(activationId);

      const deactivationId = "custom.deactivation-error";
      const deactivationError = new Error("deactivation failed");
      const deactivate = vi.fn()
        .mockImplementationOnce(() => {
          throw deactivationError;
        })
        .mockImplementation(() => undefined);
      registerCustomFeature({
        id: deactivationId,
        name: "Deactivation error",
        activate: vi.fn(),
        deactivate,
      });

      expect(activateFeature(deactivationId)).toBe(true);
      expect(deactivateFeature(deactivationId)).toBe(false);
      expect(isFeatureActive(deactivationId)).toBe(true);
      expect(getFeatureActivationError(deactivationId)).toBe(deactivationError);
      expect(unregisterFeature(deactivationId)).toBe(false);

      expect(deactivateFeature(deactivationId)).toBe(true);
      expect(getFeatureActivationError(deactivationId)).toBeUndefined();
      expect(unregisterFeature(deactivationId)).toBe(true);
      customFeatureIds.delete(deactivationId);
      expect(consoleError).toHaveBeenCalledTimes(2);
    } finally {
      consoleError.mockRestore();
    }
  });

  it("keeps debug threads inactive after event registration fails and retries cleanly", () => {
    const id = "debug.threads";
    const registrationError = new Error("thread event registration failed");
    const partialUnsubscribe = vi.fn();
    expect(deactivateFeature(id)).toBe(true);
    integrationMocks.runtimeEventsOn
      .mockReset()
      .mockReturnValueOnce(partialUnsubscribe)
      .mockImplementationOnce(() => {
        throw registrationError;
      });
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);

    try {
      expect(activateFeature(id)).toBe(false);
      expect(isFeatureActive(id)).toBe(false);
      expect(getFeatureActivationError(id)).toBe(registrationError);
      expect(getFeatureComponentLoader(id)).toBeUndefined();
      expect(partialUnsubscribe).toHaveBeenCalledTimes(1);

      integrationMocks.runtimeEventsOn
        .mockReset()
        .mockImplementation(() => vi.fn());
      expect(activateFeature(id)).toBe(true);
      expect(isFeatureActive(id)).toBe(true);
      expect(getFeatureActivationError(id)).toBeUndefined();
      expect(getFeatureComponentLoader(id)).toEqual(expect.any(Function));
    } finally {
      consoleError.mockRestore();
    }
  });
});
