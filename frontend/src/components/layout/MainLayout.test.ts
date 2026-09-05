import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";

const {
  appStateHolder,
  editorStateHolder,
  launchNodeProgramMock,
  launchCurrentFileMock,
  debugTestAtCursorMock,
  runTestAtCursorMock,
  runToolchainCommandMock,
  openAIDesktopWindowMock,
  toggleAIDesktopWindowMock,
  notifyErrorMock,
  routerPushMock,
} = vi.hoisted(() => ({
  appStateHolder: { state: null as any },
  editorStateHolder: { state: null as any },
  launchNodeProgramMock: vi.fn(),
  launchCurrentFileMock: vi.fn(),
  debugTestAtCursorMock: vi.fn(),
  runTestAtCursorMock: vi.fn(),
  runToolchainCommandMock: vi.fn(),
  openAIDesktopWindowMock: vi.fn().mockResolvedValue(undefined),
  toggleAIDesktopWindowMock: vi.fn().mockResolvedValue(undefined),
  notifyErrorMock: vi.fn(),
  routerPushMock: vi.fn(),
}));

vi.mock("vue-router", () => ({
  useRoute: () => ({ path: "/settings" }),
  useRouter: () => ({ push: routerPushMock }),
}));

vi.mock("@/stores/app", async () => {
  const { reactive } = await import("vue");
  const appState = reactive({
    activityBarVisible: false,
    statusBarVisible: false,
    sidebarCollapsed: true,
    sidebarWidth: 240,
    terminalVisible: false,
    terminalHeight: 240,
    bottomPanelView: "terminal",
    currentProject: null,
    currentFilePath: null,
    cursorLine: 1,
    cursorColumn: 1,
    minimap: false,
  });
  appStateHolder.state = appState;
  return {
    appState,
    setPanelTab: vi.fn(),
    setExtensionsSubview: vi.fn(),
    toggleTerminal: vi.fn(),
    toggleActivityBar: vi.fn(),
    toggleStatusBar: vi.fn(),
    saveSettings: vi.fn(),
    openProject: vi.fn(),
  };
});

vi.mock("@/stores/editor", async () => {
  const { computed, reactive } = await import("vue");
  const editorState = reactive({
    openFiles: [] as Array<{
      path: string;
      name: string;
      content: string;
      originalContent: string;
      language: string;
      isDirty: boolean;
    }>,
    activeFilePath: null as string | null,
  });
  editorStateHolder.state = editorState;
  return {
    editorState,
    activeFile: computed(
      () =>
        editorState.openFiles.find(
          (file) => file.path === editorState.activeFilePath,
        ) ?? null,
    ),
    saveFile: vi.fn(),
    saveAllFiles: vi.fn().mockResolvedValue(0),
    updateContent: vi.fn(),
  };
});

vi.mock("@/stores/debug", () => ({
  launchNodeProgram: launchNodeProgramMock,
  launchCurrentFile: launchCurrentFileMock,
  debugTestAtCursor: debugTestAtCursorMock,
  restartDebugSession: vi.fn(),
  launchDebugPackage: vi.fn(),
  refreshDebugStatus: vi.fn().mockResolvedValue(undefined),
}));

vi.mock("@/stores/toolchain", async () => {
  const { reactive } = await import("vue");
  return {
    toolchainState: reactive({
      commands: [{ id: "lint-active", label: "Lint active file" }],
      running: false,
      runningId: null,
      detected: {},
    }),
    loadToolchainCommands: vi.fn(),
    runToolchainCommand: runToolchainCommandMock,
    runTestAtCursor: runTestAtCursorMock,
  };
});

vi.mock("@/stores/ai", () => ({ clearMessages: vi.fn() }));
vi.mock("@/stores/aiAssistant", () => ({
  openAIDesktopWindow: openAIDesktopWindowMock,
  toggleAIDesktopWindow: toggleAIDesktopWindowMock,
}));
vi.mock("@/lib/notifications", () => ({
  notifyError: notifyErrorMock,
}));
vi.mock("@/stores/inlineCompletion", () => ({
  toggleInlineCompletion: vi.fn(),
}));
vi.mock("@/composables/useKeyboard", () => ({
  useKeyboard: vi.fn(),
  registerShortcut: vi.fn(),
}));
vi.mock("@/composables/useDragResize", () => ({
  useDragResize: () => ({
    getCurrentValue: () => 240,
    ariaMin: 80,
    ariaMax: 600,
    onPointerDown: vi.fn(),
    onKeyDown: vi.fn(),
  }),
}));
vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
}));
vi.mock("@/stores/layout", async () => {
  const { reactive } = await import("vue");
  return {
    layoutState: reactive({
      tree: {
        root: { id: "leaf-1", type: "leaf", viewId: "editor" },
        activeLeafId: "leaf-1",
      },
    }),
    setActiveLeaf: vi.fn(),
    saveLayoutToBackend: vi.fn(),
  };
});
vi.mock("@/api/services", () => ({
  layoutService: { saveLayout: vi.fn(), resetLayout: vi.fn() },
  projectService: { addProject: vi.fn() },
  windowService: { toggleAIWindow: vi.fn() },
}));
vi.mock("@/lib/unifiedCommands", () => ({
  getUnifiedPaletteCommands: () => [],
}));
vi.mock("@/stores/refactor", async () => {
  const { reactive } = await import("vue");
  return {
    REFACTOR_COMMANDS: [],
    cancelRefactorRequest: vi.fn(),
    refactorState: reactive({ loading: false, available: {} }),
    refreshRefactorActions: vi.fn(),
    selectRefactorCommand: vi.fn(),
  };
});
vi.mock("@/stores/lsp", () => ({ monacoLanguageToLSP: vi.fn() }));

vi.mock("./ActivityBar.vue", () => ({ default: { template: "<div />" } }));
vi.mock("./TitleBar.vue", () => ({ default: { template: "<div />" } }));
vi.mock("./SidePanel.vue", () => ({ default: { template: "<div />" } }));
vi.mock("./TerminalPanel.vue", () => ({ default: { template: "<div />" } }));
vi.mock("./StatusBar.vue", () => ({ default: { template: "<div />" } }));
vi.mock("./CommandPalette.vue", () => ({
  default: {
    name: "CommandPalette",
    props: ["commands", "visible"],
    template: "<div class='command-palette-stub' />",
  },
}));
vi.mock("./QuickOpen.vue", () => ({ default: { template: "<div />" } }));
vi.mock("./WorkspaceSymbolPicker.vue", () => ({
  default: { template: "<div />" },
}));
vi.mock("./LayoutLeafView.vue", () => ({ default: { template: "<div />" } }));
vi.mock("./LayoutSplitView.vue", () => ({ default: { template: "<div />" } }));
vi.mock("../modals/NewProjectWizard.vue", () => ({
  default: { template: "<div />" },
}));
vi.mock("../modals/ApplyDiffModal.vue", () => ({
  default: { template: "<div />" },
}));
vi.mock("../modals/RefactorPreviewModal.vue", () => ({
  default: { template: "<div />" },
}));

import MainLayout from "./MainLayout.vue";
import { commandRegistry } from "@/lib/commands";

let wrapper: VueWrapper | null = null;

describe("MainLayout active-file commands", () => {
  beforeEach(() => {
    commandRegistry.clear();
    launchNodeProgramMock.mockClear();
    launchCurrentFileMock.mockClear();
    debugTestAtCursorMock.mockClear();
    runTestAtCursorMock.mockClear();
    runToolchainCommandMock.mockClear();
    openAIDesktopWindowMock.mockReset().mockResolvedValue(undefined);
    toggleAIDesktopWindowMock.mockReset().mockResolvedValue(undefined);
    notifyErrorMock.mockClear();
    routerPushMock.mockClear();

    editorStateHolder.state.openFiles = [
      {
        path: "/workspace/active_test.go",
        name: "active_test.go",
        content: "package active",
        originalContent: "package active",
        language: "go",
        isDirty: false,
      },
    ];
    editorStateHolder.state.activeFilePath = "/workspace/active_test.go";
    appStateHolder.state.currentFilePath = "/workspace/stale.go";
    appStateHolder.state.cursorLine = 7;
    appStateHolder.state.cursorColumn = 3;

    wrapper = mount(MainLayout);
  });

  afterEach(() => {
    wrapper?.unmount();
    wrapper = null;
    commandRegistry.clear();
  });

  it("passes the editor active path to debug, cursor-test, and toolchain commands", async () => {
    expect(await commandRegistry.execute("debug-current-file")).toBe(true);
    await flushPromises();
    expect(launchCurrentFileMock).toHaveBeenCalledWith(
      "/workspace/active_test.go",
      [],
    );

    expect(await commandRegistry.execute("debug-node")).toBe(true);
    await flushPromises();
    expect(launchNodeProgramMock).toHaveBeenCalledWith(
      "/workspace/active_test.go",
      [],
    );

    expect(await commandRegistry.execute("test-at-cursor")).toBe(true);
    await flushPromises();
    expect(runTestAtCursorMock).toHaveBeenCalledWith(
      "go",
      "/workspace/active_test.go",
      6,
      "package active",
    );

    expect(await commandRegistry.execute("debug-test-at-cursor")).toBe(true);
    await flushPromises();
    expect(debugTestAtCursorMock).toHaveBeenCalledWith(
      "go",
      "/workspace/active_test.go",
      6,
      "package active",
    );

    expect(await commandRegistry.execute("toolchain-lint-active")).toBe(true);
    expect(runToolchainCommandMock).toHaveBeenCalledWith(
      "lint-active",
      "/workspace/active_test.go",
    );
  });

  it("opens or focuses AI through the conversation handoff without toggling it hidden", async () => {
    expect(await commandRegistry.execute("toggle-ai")).toBe(true);
    await flushPromises();

    expect(openAIDesktopWindowMock).toHaveBeenCalledOnce();
    expect(toggleAIDesktopWindowMock).not.toHaveBeenCalled();
  });

  it("reports AI handoff failures instead of dropping the rejected promise", async () => {
    openAIDesktopWindowMock.mockRejectedValueOnce(new Error("AI window unavailable"));

    expect(await commandRegistry.execute("toggle-ai")).toBe(true);
    await flushPromises();

    expect(notifyErrorMock).toHaveBeenCalledWith("AI window unavailable");
  });

  it("opens Debug and Test Explorer views from palette command ids", async () => {
    const palette = wrapper!.findComponent({ name: "CommandPalette" });
    const commands = palette.props("commands") as Array<{ id: string; action: () => void }>;
    const debug = commands.find((command) => command.id === "koyoriIde.view.debug");
    const tests = commands.find((command) => command.id === "koyoriIde.view.testExplorer");
    expect(debug).toBeTruthy();
    expect(tests).toBeTruthy();
    debug!.action();
    tests!.action();
    expect(routerPushMock).toHaveBeenCalledWith("/debug");
    expect(routerPushMock).toHaveBeenCalledWith("/test");
  });

  it("opens advanced side panels from palette command ids", async () => {
    const { setPanelTab } = await import("@/stores/app");
    const palette = wrapper!.findComponent({ name: "CommandPalette" });
    const commands = palette.props("commands") as Array<{ id: string; action: () => void }>;
    for (const id of [
      "koyoriIde.view.build",
      "koyoriIde.view.database",
      "koyoriIde.view.httpClient",
      "koyoriIde.view.inspections",
      "koyoriIde.view.callHierarchy",
    ]) {
      const command = commands.find((entry) => entry.id === id);
      expect(command, id).toBeTruthy();
      command!.action();
    }
    expect(setPanelTab).toHaveBeenCalledWith("build");
    expect(setPanelTab).toHaveBeenCalledWith("database");
    expect(setPanelTab).toHaveBeenCalledWith("httpClient");
    expect(setPanelTab).toHaveBeenCalledWith("inspections");
    expect(setPanelTab).toHaveBeenCalledWith("callHierarchy");
    expect(routerPushMock).toHaveBeenCalledWith("/editor");
  });
});
