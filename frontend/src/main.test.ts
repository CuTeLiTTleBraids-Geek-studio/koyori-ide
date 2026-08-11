/**
 * L-6: Tests for unregisterGlobalErrorHandlers in main.ts.
 *
 * main.ts is the app entry point with many side effects (createApp,
 * bootstrap, monaco loader config, etc.). We mock the heavy transitive
 * deps so importing it only exercises the error-handler registration
 * logic we want to test.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// Mock all heavy / side-effectful imports so main.ts can be imported
// in a test without mounting the real app or pulling in monaco/wails.
vi.mock("vue", () => ({
  createApp: () => ({
    component: () => {},
    use: () => {},
    mount: () => {},
    config: { errorHandler: null as ((err: unknown, ...args: unknown[]) => void) | null },
  }),
}));
vi.mock("element-plus", () => ({ default: { install: () => {} } }));
vi.mock("element-plus/dist/index.css", () => ({}));
vi.mock("element-plus/theme-chalk/dark/css-vars.css", () => ({}));
vi.mock("@element-plus/icons-vue", () => ({ default: {} }));
vi.mock("animate.css", () => ({}));
vi.mock("@/assets/styles/main.css", () => ({}));
vi.mock("@/App.vue", () => ({ default: {} }));
vi.mock("@/router", () => ({ default: {} }));
vi.mock("@/stores/app", () => ({
  loadSettings: vi.fn().mockResolvedValue(undefined),
  initThemes: vi.fn(),
  initAppActions: vi.fn(),
  initAppStateEffects: vi.fn(),
  startSystemModeListener: vi.fn(),
  stopSystemModeListener: vi.fn(),
  teardownAppStateEffects: vi.fn(),
  initWindowMaximiseListener: vi.fn(),
  appState: { aiBaseUrl: "", currentProject: "", enablePluginSandbox: false, personalization: {} },
}));
vi.mock("@/lib/pluginRegistry", () => ({
  setSandboxMode: vi.fn(),
  deactivateAllPlugins: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("@/lib/pluginSandbox", () => ({
  resetPluginSandboxState: vi.fn(),
}));
vi.mock("@/stores/inlineCompletion", () => ({
  cleanupInlineCompletion: vi.fn(),
}));
vi.mock("@/stores/tasks", () => ({
  cleanupTaskStoreTimers: vi.fn(),
}));
vi.mock("@/stores/agent", () => ({
  initAgentPendingSyncListener: vi.fn(),
  cleanupAgentPendingSyncListener: vi.fn(),
}));
vi.mock("@/stores/debug", () => ({
  initDebugRuntime: vi.fn(),
  cleanupDebugRuntime: vi.fn(),
}));
vi.mock("@/stores/testExplorer", () => ({
  initTestExplorerRuntime: vi.fn(),
  teardownContinuousTesting: vi.fn(),
}));
vi.mock("@/stores/layout", () => ({
  loadLayoutFromBackend: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("@/stores/workflows", () => ({
  loadWorkflows: vi.fn().mockResolvedValue(undefined),
  cleanupWorkflowRuntime: vi.fn(),
}));
vi.mock("@/stores/plugins", () => ({
  loadPlugins: vi.fn().mockResolvedValue(undefined),
  activateStartupPlugins: vi.fn().mockResolvedValue(undefined),
}));
vi.mock("@/api/services", () => ({
  layoutService: { loadLayout: vi.fn().mockResolvedValue(null) },
}));
vi.mock("@/lib/notifications", () => ({
  notifyError: vi.fn(),
}));
vi.mock("@/stores/output", () => ({
  pushOutput: vi.fn(),
}));
vi.mock("@/lib/errors", () => ({
  isCancellationError: vi.fn().mockReturnValue(false),
}));
vi.mock("@/lib/connectivity", () => ({
  initConnectivityListener: vi.fn(),
  stopConnectivityListener: vi.fn(),
}));
vi.mock("@/lib/crossWindowSync", () => ({
  initCrossWindowSync: vi.fn(),
  teardownCrossWindowSync: vi.fn(),
}));
vi.mock("@/lib/vscodeExtensionActivation", () => ({
  resetActivationState: vi.fn(),
}));
vi.mock("@/lib/monacoExtensionContributes", () => ({
  clearMonacoExtensionContributes: vi.fn(),
}));
vi.mock("@/lib/codeLens", () => ({
  cleanupCodeLensProviders: vi.fn(),
}));
vi.mock("@/lib/inlayHints", () => ({
  cleanupInlayHintsProviders: vi.fn(),
}));
vi.mock("@/lib/semanticTokens", () => ({
  cleanupSemanticTokensProviders: vi.fn(),
}));
vi.mock("@/stores/lsp", () => ({
  initLSPStore: vi.fn().mockResolvedValue(undefined),
  cleanupPullDiagnostics: vi.fn(),
}));
vi.mock("@/stores/ai", () => ({
  cleanupAIEventListeners: vi.fn(),
  ensureAIEventListeners: vi.fn(),
}));
vi.mock("@/composables/usePersonalization", () => ({
  initPersonalization: vi.fn(),
  teardownPersonalization: vi.fn(),
}));
vi.mock("@wailsio/runtime", () => ({
  Events: { On: vi.fn() },
}));
vi.mock("@/stores/editor", () => ({
  requestApplyToEditor: vi.fn(),
}));
vi.mock("@/stores/snapshot", () => ({
  setSnapshotWorkspaceRoot: vi.fn(),
  resetSnapshotStore: vi.fn(),
}));
vi.mock("@/lib/i18n", () => ({
  translate: vi.fn().mockReturnValue(""),
}));
vi.mock("@guolao/vue-monaco-editor", () => ({
  loader: { config: () => {} },
}));
vi.mock("monaco-editor", () => ({
  default: {},
  editor: {},
  languages: {},
}));
// Worker imports — return dummy constructors.
vi.mock("monaco-editor/esm/vs/editor/editor.worker?worker", () => ({
  default: function () {
    return {};
  },
}));
vi.mock("monaco-editor/esm/vs/language/json/json.worker?worker", () => ({
  default: function () {
    return {};
  },
}));
vi.mock("monaco-editor/esm/vs/language/css/css.worker?worker", () => ({
  default: function () {
    return {};
  },
}));
vi.mock("monaco-editor/esm/vs/language/html/html.worker?worker", () => ({
  default: function () {
    return {};
  },
}));
vi.mock("monaco-editor/esm/vs/language/typescript/ts.worker?worker", () => ({
  default: function () {
    return {};
  },
}));

import {
  bootstrapFrontendRuntime,
  disposeFrontendRuntime,
  unregisterGlobalErrorHandlers,
} from "./main";

type FrontendRuntimeGlobal = typeof globalThis & {
  __koyoriIdeFrontendRuntimeOwner?: symbol | null;
  __koyoriIdeAcquireFrontendRuntime?: (owner: symbol) => () => void;
};

const frontendRuntimeGlobal = globalThis as FrontendRuntimeGlobal;

describe("L-6: unregisterGlobalErrorHandlers", () => {
  beforeEach(() => {
    vi.useRealTimers();
    vi.clearAllMocks();
  });

  afterEach(() => {
    // Re-register handlers so subsequent test files that import main.ts
    // still have them (module state persists across test files).
    vi.restoreAllMocks();
  });

  it("removes the error and unhandledrejection listeners from window", () => {
    const removeSpy = vi.spyOn(window, "removeEventListener");
    // main.ts registers handlers on import; calling unregister should
    // remove both the "error" and "unhandledrejection" listeners.
    unregisterGlobalErrorHandlers();
    const removedTypes = removeSpy.mock.calls.map((c) => c[0]);
    expect(removedTypes).toContain("error");
    expect(removedTypes).toContain("unhandledrejection");
  });

  it("is idempotent — calling twice does not throw", () => {
    unregisterGlobalErrorHandlers();
    expect(() => unregisterGlobalErrorHandlers()).not.toThrow();
  });

  it("after unregister, dispatching an error event does not invoke pushOutput", async () => {
    const { pushOutput } = await import("@/stores/output");
    (pushOutput as ReturnType<typeof vi.fn>).mockClear();

    unregisterGlobalErrorHandlers();
    const event = new ErrorEvent("error", { message: "post-unregister error" });
    window.dispatchEvent(event);

    // Give microtasks a chance to flush (handlers are synchronous, but
    // just in case any async path exists).
    await new Promise((r) => setTimeout(r, 10));
    expect(pushOutput).not.toHaveBeenCalled();
  });

  it("disposes every entry-level listener and provider during HMR", async () => {
    const app = await import("@/stores/app");
    const personalization = await import("@/composables/usePersonalization");
    const connectivity = await import("@/lib/connectivity");
    const crossWindow = await import("@/lib/crossWindowSync");
    const activation = await import("@/lib/vscodeExtensionActivation");
    const contributes = await import("@/lib/monacoExtensionContributes");
    const codeLens = await import("@/lib/codeLens");
    const inlayHints = await import("@/lib/inlayHints");
    const semanticTokens = await import("@/lib/semanticTokens");
    const lsp = await import("@/stores/lsp");
    const plugins = await import("@/lib/pluginRegistry");
    const sandbox = await import("@/lib/pluginSandbox");
    const inlineCompletion = await import("@/stores/inlineCompletion");
    const tasks = await import("@/stores/tasks");
    const debug = await import("@/stores/debug");
    const testExplorer = await import("@/stores/testExplorer");
    const snapshot = await import("@/stores/snapshot");
    const workflows = await import("@/stores/workflows");

    disposeFrontendRuntime();
    disposeFrontendRuntime();

    expect(app.stopSystemModeListener).toHaveBeenCalledOnce();
    expect(app.teardownAppStateEffects).toHaveBeenCalledOnce();
    expect(personalization.teardownPersonalization).toHaveBeenCalledOnce();
    expect(connectivity.stopConnectivityListener).toHaveBeenCalledOnce();
    expect(crossWindow.teardownCrossWindowSync).toHaveBeenCalledOnce();
    expect(activation.resetActivationState).toHaveBeenCalledOnce();
    expect(contributes.clearMonacoExtensionContributes).toHaveBeenCalledOnce();
    expect(codeLens.cleanupCodeLensProviders).toHaveBeenCalledOnce();
    expect(inlayHints.cleanupInlayHintsProviders).toHaveBeenCalledOnce();
    expect(semanticTokens.cleanupSemanticTokensProviders).toHaveBeenCalledOnce();
    expect(lsp.cleanupPullDiagnostics).toHaveBeenCalledOnce();
    expect(plugins.deactivateAllPlugins).toHaveBeenCalledOnce();
    expect(sandbox.resetPluginSandboxState).toHaveBeenCalledOnce();
    expect(inlineCompletion.cleanupInlineCompletion).toHaveBeenCalledOnce();
    expect(tasks.cleanupTaskStoreTimers).toHaveBeenCalledOnce();
    expect(debug.cleanupDebugRuntime).toHaveBeenCalledOnce();
    expect(testExplorer.teardownContinuousTesting).toHaveBeenCalledOnce();
    expect(snapshot.resetSnapshotStore).toHaveBeenCalledOnce();
    expect(workflows.cleanupWorkflowRuntime).toHaveBeenCalledOnce();
  });

  it("keeps the runtime alive across an App-only HMR replacement", async () => {
    const app = await import("@/stores/app");
    disposeFrontendRuntime();
    await bootstrapFrontendRuntime();

    const owner = frontendRuntimeGlobal.__koyoriIdeFrontendRuntimeOwner;
    const acquire = frontendRuntimeGlobal.__koyoriIdeAcquireFrontendRuntime;
    expect(typeof owner).toBe("symbol");
    expect(acquire).toBeTypeOf("function");

    const releaseOldApp = acquire!(owner!);
    vi.clearAllMocks();
    releaseOldApp();
    const releaseReplacementApp = acquire!(owner!);
    await Promise.resolve();

    expect(frontendRuntimeGlobal.__koyoriIdeFrontendRuntimeOwner).toBe(owner);
    expect(app.stopSystemModeListener).not.toHaveBeenCalled();

    releaseReplacementApp();
    await Promise.resolve();
    expect(app.stopSystemModeListener).toHaveBeenCalledOnce();
  });

  it("does not let an old App lease dispose a successor runtime", async () => {
    const app = await import("@/stores/app");
    disposeFrontendRuntime();
    await bootstrapFrontendRuntime();

    const oldOwner = frontendRuntimeGlobal.__koyoriIdeFrontendRuntimeOwner;
    const oldAcquire = frontendRuntimeGlobal.__koyoriIdeAcquireFrontendRuntime;
    expect(typeof oldOwner).toBe("symbol");
    const releaseOldApp = oldAcquire!(oldOwner!);

    await bootstrapFrontendRuntime();
    const successorOwner = frontendRuntimeGlobal.__koyoriIdeFrontendRuntimeOwner;
    expect(successorOwner).not.toBe(oldOwner);
    vi.clearAllMocks();

    releaseOldApp();
    await Promise.resolve();

    expect(frontendRuntimeGlobal.__koyoriIdeFrontendRuntimeOwner).toBe(successorOwner);
    expect(app.stopSystemModeListener).not.toHaveBeenCalled();
    disposeFrontendRuntime(successorOwner ?? null);
  });

  it("does not revive runtime resources when an old bootstrap finishes after dispose", async () => {
    const app = await import("@/stores/app");
    const personalization = await import("@/composables/usePersonalization");
    const connectivity = await import("@/lib/connectivity");
    let releaseSettings: () => void = () => undefined;
    const pendingSettings = new Promise<void>((resolve) => {
      releaseSettings = resolve;
    });
    (app.loadSettings as ReturnType<typeof vi.fn>).mockReturnValueOnce(pendingSettings);

    const bootstrap = bootstrapFrontendRuntime();
    disposeFrontendRuntime();
    releaseSettings();
    await bootstrap;

    expect(personalization.initPersonalization).not.toHaveBeenCalled();
    expect(connectivity.initConnectivityListener).not.toHaveBeenCalled();
  });

  it("restarts app-level watchers when a fresh runtime takes ownership", async () => {
    const app = await import("@/stores/app");
    const crossWindow = await import("@/lib/crossWindowSync");
    const debug = await import("@/stores/debug");
    const testExplorer = await import("@/stores/testExplorer");

    disposeFrontendRuntime();
    vi.clearAllMocks();
    await bootstrapFrontendRuntime();

    expect(app.initAppActions).toHaveBeenCalledOnce();
    expect(app.initAppStateEffects).toHaveBeenCalledOnce();
    expect(crossWindow.initCrossWindowSync).toHaveBeenCalledOnce();
    expect(debug.initDebugRuntime).toHaveBeenCalledOnce();
    expect(testExplorer.initTestExplorerRuntime).toHaveBeenCalledOnce();
  });

  it("does not let an old LSP completion clear a successor runtime", async () => {
    const lsp = await import("@/stores/lsp");
    let finishOldLsp: () => void = () => undefined;
    const oldLsp = new Promise<void>((resolve) => {
      finishOldLsp = resolve;
    });
    (lsp.initLSPStore as ReturnType<typeof vi.fn>)
      .mockReturnValueOnce(oldLsp)
      .mockResolvedValueOnce(undefined);

    await bootstrapFrontendRuntime();
    disposeFrontendRuntime();
    await bootstrapFrontendRuntime();
    const cleanupCallsAfterSuccessor = (lsp.cleanupPullDiagnostics as ReturnType<typeof vi.fn>).mock.calls.length;

    finishOldLsp();
    await Promise.resolve();
    await Promise.resolve();

    expect(lsp.cleanupPullDiagnostics).toHaveBeenCalledTimes(cleanupCallsAfterSuccessor);
  });

  it("waits for plugin teardown before loading plugins in a successor", async () => {
    const pluginRegistry = await import("@/lib/pluginRegistry");
    const pluginStore = await import("@/stores/plugins");
    let finishTeardown: () => void = () => undefined;
    const teardown = new Promise<void>((resolve) => {
      finishTeardown = resolve;
    });
    (pluginRegistry.deactivateAllPlugins as ReturnType<typeof vi.fn>)
      .mockReturnValueOnce(teardown);

    disposeFrontendRuntime();
    const bootstrap = bootstrapFrontendRuntime();
    await Promise.resolve();
    await Promise.resolve();
    expect(pluginStore.loadPlugins).not.toHaveBeenCalled();

    finishTeardown();
    await bootstrap;

    expect(pluginStore.loadPlugins).toHaveBeenCalledOnce();
  });
});
