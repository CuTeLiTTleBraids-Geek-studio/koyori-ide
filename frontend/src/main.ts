// Koyori IDE（こより IDE）前端入口。
//
// 这个文件是 Vue 3 应用的 bootstrap：创建根实例、注册 Element Plus
// 与全局图标、挂载路由，并协调前后端运行时的「单例所有权」——
// Koyori IDE 可能同时运行主窗口与 AI 伴侣窗口，同一个前端 bundle
// 需要分清谁是真正的 IDE 运行时（main/e2e），谁只是附属窗口
// （ai/settings/minimal）。运行时所有权通过 globalThis 上的
// __koyoriIde* 钩子传递，见 runtimeRole.ts。
import { createApp } from "vue";
import ElementPlus from "element-plus";
import "element-plus/dist/index.css";
import "element-plus/theme-chalk/dark/css-vars.css";
import * as ElementPlusIconsVue from "@element-plus/icons-vue";
import "animate.css";
import "./assets/styles/main.css";
import App from "./App.vue";
import router from "./router";
import {
  loadSettings,
  initThemes,
  initAppActions,
  initAppStateEffects,
  startSystemModeListener,
  stopSystemModeListener,
  teardownAppStateEffects,
  appState,
} from "@/stores/app";
import {
  initCrossWindowSync,
  teardownCrossWindowSync,
} from "@/lib/crossWindowSync";
import { deactivateAllPlugins, setSandboxMode } from "@/lib/pluginRegistry";
import { loadLayoutFromBackend } from "@/stores/layout";
import {
  cleanupWorkflowRuntime,
  loadWorkflows,
} from "@/stores/workflows";
import { loadPlugins, activateStartupPlugins } from "@/stores/plugins";
import { layoutService } from "@/api/services";
import { languagePackService } from "@/api/languagePacks";
import { notifyError } from "@/lib/notifications";
import { pushOutput } from "@/stores/output";
import { isCancellationError } from "@/lib/errors";
import {
  initConnectivityListener,
  stopConnectivityListener,
} from "@/lib/connectivity";
import { cleanupPullDiagnostics, initLSPStore } from "@/stores/lsp";
// Plan 11 Task 15: 个性化运行时（背景图/字体/气泡 CSS 变量应用）
import {
  initPersonalization,
  teardownPersonalization,
} from "@/composables/usePersonalization";
import { resetSnapshotStore, setSnapshotWorkspaceRoot } from "@/stores/snapshot";
import { cleanupCodeLensProviders } from "@/lib/codeLens";
import { cleanupInlayHintsProviders } from "@/lib/inlayHints";
import { cleanupSemanticTokensProviders } from "@/lib/semanticTokens";
import { resetActivationState } from "@/lib/vscodeExtensionActivation";
import { clearMonacoExtensionContributes } from "@/lib/monacoExtensionContributes";
import { resetPluginSandboxState } from "@/lib/pluginSandbox";
import { cleanupInlineCompletion } from "@/stores/inlineCompletion";
import { cleanupTaskStoreTimers } from "@/stores/tasks";
import { cleanupDebugRuntime, initDebugRuntime } from "@/stores/debug";
import {
  initTestExplorerRuntime,
  teardownContinuousTesting,
} from "@/stores/testExplorer";
import * as WindowServiceBindings from "../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/windowservice.js";
import {
  isFullIDERuntimeRole,
  normalizeRuntimeRole,
  resolveRuntimeRoleToken,
  type FrontendRuntimeRole,
} from "./runtimeRole";

if (__KOYORI_IDE_E2E_HTTP_CLIENT__ === "1") {
  void import("@/e2e/httpClientProbe").then(({ installHTTPClientProbe }) => {
    installHTTPClientProbe();
  });
}

if (__KOYORI_IDE_E2E_RECOVERY__ === "1") {
  void import("@/e2e/recoveryProbe").then(({ installRecoveryProbe }) => {
    installRecoveryProbe();
  });
}

if (__KOYORI_IDE_E2E_WORKSPACE__ === "1") {
  void import("@/e2e/workspaceProbe").then(({ installWorkspaceProbe }) => {
    installWorkspaceProbe();
  });
}

if (__KOYORI_IDE_E2E_RUNTIME_ROLE__ === "1") {
  void import("@/e2e/runtimeRoleProbe").then(({ installRuntimeRoleProbe }) => {
    installRuntimeRoleProbe();

  });
}

// P9-G10: opt-in packaged Monaco probe (VITE_KOYORI_IDE_E2E_MONACO=1).
if (__KOYORI_IDE_E2E_MONACO__ === "1") {
void import("@/e2e/monacoProbe").then(({ installMonacoProbe }) => {
  installMonacoProbe();
});
}

// P9-G13: opt-in packaged extension API no-fake-success probe (same opt-in flag).
if (__KOYORI_IDE_E2E_MONACO__ === "1") {
void import("@/e2e/extensionApiProbe").then(({ installExtensionApiProbe }) => {
  installExtensionApiProbe();
});
}

// P9-G15: opt-in packaged Test Explorer runner/state probe.
if (__KOYORI_IDE_E2E_MONACO__ === "1") {
void import("@/e2e/testExplorerProbe").then(({ installTestExplorerProbe }) => {
  installTestExplorerProbe();
});
}

// P9-G16: opt-in packaged terminal exit/reconnect probe.
if (__KOYORI_IDE_E2E_MONACO__ === "1") {
  void import("@/e2e/terminalReconnectProbe").then(({ installTerminalReconnectProbe }) => {
    installTerminalReconnectProbe();
  });
}

// P9-G24: opt-in packaged independent Extension Host/lifecycle probe.
if (__KOYORI_IDE_E2E_MONACO__ === "1") {
  void import("@/e2e/extensionHostG24Probe").then(({ installExtensionHostG24Probe }) => {
    installExtensionHostG24Probe();
  });
}

// Monaco editor: configure the @guolao/vue-monaco-editor loader to use the
// locally bundled monaco-editor package instead of loading from CDN.
// Without this, the loader tries to fetch the AMD bundle from
// https://cdn.jsdelivr.net/npm/monaco-editor@.../min/vs/loader.js, which
// the app's strict CSP (connect-src 'self', script-src 'self' 'nonce-<N>')
// blocks — causing a "load failed" error when opening any file.
// We also set up MonacoEnvironment so web workers are bundled by Vite
// via ?worker imports instead of being fetched from CDN at runtime.
import { loader } from "@guolao/vue-monaco-editor";
import * as monaco from "monaco-editor";
import editorWorker from "monaco-editor/esm/vs/editor/editor.worker?worker";
import jsonWorker from "monaco-editor/esm/vs/language/json/json.worker?worker";
import cssWorker from "monaco-editor/esm/vs/language/css/css.worker?worker";
import htmlWorker from "monaco-editor/esm/vs/language/html/html.worker?worker";
import tsWorker from "monaco-editor/esm/vs/language/typescript/ts.worker?worker";

loader.config({ monaco });

self.MonacoEnvironment = {
  getWorker(_workerId: string, label: string) {
    if (label === "json") return new jsonWorker();
    if (label === "css") return new cssWorker();
    if (label === "html") return new htmlWorker();
    if (label === "typescript" || label === "javascript") return new tsWorker();
    return new editorWorker();
  },
};

/**
 * Proposal F (prompt-4.md): Explicit bootstrap sequence.
 *
 * Each stage is clearly named so that "island code" (modules with init
 * functions that exist but are never called) is visible at a glance.
 * Stages are ordered by dependency: themes → settings → sandbox → plugins → layout.
 * Each stage's failure is logged but does not block subsequent stages
 * (except settings, which sandbox depends on).
 *
 * N-118 / Proposal AD: the whole bootstrap() is wrapped in try/catch so
 * that a failure in any stage (loadSettings, loadLayoutFromBackend,
 * loadWorkflows, etc.) is surfaced to the user instead of being silently
 * swallowed by the `void bootstrap()` fire-and-forget call.
 */
const runtimeGlobal = globalThis as typeof globalThis & {
  __koyoriIdeFrontendRuntimeOwner?: symbol | null;
  __koyoriIdeRuntimeRole?: FrontendRuntimeRole;
  __koyoriIdeFrontendBootstrap?: { role: FrontendRuntimeRole; stages: string[] };
  __koyoriIdePluginTeardown?: Promise<void>;
  __koyoriIdeAcquireFrontendRuntime?: (owner: symbol) => () => void;
  __koyoriIdeDisposeFrontendRuntime?: (owner: symbol) => void;
};
let activeRuntimeOwner: symbol | null = null;
let runtimeConsumerCount = 0;
let runtimeReleaseGeneration = 0;

export function getFrontendRuntimeRole(): FrontendRuntimeRole {
  return runtimeGlobal.__koyoriIdeRuntimeRole ?? "minimal";
}

function recordBootstrapStage(stage: string): void {
  const trace = runtimeGlobal.__koyoriIdeFrontendBootstrap;
  if (!trace) return;
  trace.stages.push(stage);
  if (typeof document !== "undefined") {
    document.documentElement.dataset.koyoriIdeRuntimeRole = trace.role;
    document.documentElement.dataset.koyoriIdeBootstrapStages = trace.stages.join(",");
  }
}

function isCurrentRuntimeOwner(owner: symbol | null): owner is symbol {
  return Boolean(
    owner &&
    activeRuntimeOwner === owner &&
    runtimeGlobal.__koyoriIdeFrontendRuntimeOwner === owner,
  );
}

export async function bootstrapFrontendRuntime(
  role: FrontendRuntimeRole = "main",
): Promise<void> {
  const runtimeOwner = Symbol("koyori-ide-frontend-runtime");
  activeRuntimeOwner = runtimeOwner;
  runtimeConsumerCount = 0;
  runtimeReleaseGeneration += 1;
  runtimeGlobal.__koyoriIdeFrontendRuntimeOwner = runtimeOwner;
  runtimeGlobal.__koyoriIdeRuntimeRole = role;
  runtimeGlobal.__koyoriIdeFrontendBootstrap = { role, stages: [] };
  recordBootstrapStage("role-resolved");
  runtimeGlobal.__koyoriIdeAcquireFrontendRuntime = acquireFrontendRuntimeHook;
  runtimeGlobal.__koyoriIdeDisposeFrontendRuntime = disposeFrontendRuntimeHook;
  registerGlobalErrorHandlers();
  const isStale = () => runtimeGlobal.__koyoriIdeFrontendRuntimeOwner !== runtimeOwner;
  const hasNoSuccessor = () => runtimeGlobal.__koyoriIdeFrontendRuntimeOwner == null;
  try {
    const fullIDE = isFullIDERuntimeRole(role);
    if (fullIDE) {
      initDebugRuntime();
      recordBootstrapStage("debug-runtime");
      initTestExplorerRuntime();
      recordBootstrapStage("test-explorer-runtime");
    }

    // Stage 1: Initialize themes (sync, no I/O).
    initThemes();
    recordBootstrapStage("themes");

    // Stage 2: Start system mode listener (sync, listens for OS theme changes).
    startSystemModeListener();
    // 架构改进 B (prompt-1.md): 跨窗口事件统一由 lib/crossWindowSync.ts 编排。
    // 包含 window:maximised / settings:changed / project:removed / ai:apply-to-editor。
    // 放在 bootstrap 早期，确保 TitleBar 挂载时最大化状态已就绪。
    initCrossWindowSync();
    recordBootstrapStage("cross-window-sync");
    if (fullIDE) {
      initAppActions();
      initAppStateEffects();
      recordBootstrapStage("main-runtime-effects");
    }

    // Stage 3: Load settings (async, reads settings.json from profile dir).
    // This also hydrates appState with the user's preferences.
    await loadSettings(() => !isStale());
    if (isStale()) return;
    recordBootstrapStage("settings");

    // Stage 3.1 (Task 15): apply personalization CSS variables now that
    // appState.personalization is hydrated, and watch for later changes.
    try {
      initPersonalization();
      recordBootstrapStage("personalization");
    } catch (e: unknown) {
      console.error("Personalization init failed:", e);
    }

    // Stage 3.5 (G-FEAT-02): Start connectivity listener now that aiBaseUrl
    // is hydrated, and probe for installed LSP servers (gopls / tsserver).
    // Both are best-effort and must never block bootstrap — failures only
    // mean offline completion is unavailable, not that the IDE won't start.
    if (fullIDE) {
      try {
        await languagePackService.refreshRuntime();
        if (isStale()) return;
        recordBootstrapStage("language-packs");
      } catch (e: unknown) {
        console.error("Language pack runtime init failed:", e);
      }
      try {
        initConnectivityListener();
        recordBootstrapStage("connectivity");
      } catch (e: unknown) {
        console.error("Connectivity listener init failed:", e);
      }
      try {
        void initLSPStore()
          .then(() => {
            if (isStale() && hasNoSuccessor()) cleanupPullDiagnostics();
          })
          .catch((e: unknown) => {
            if (!isStale()) console.error("LSP store init failed:", e);
          });
        recordBootstrapStage("lsp");
      } catch (e: unknown) {
        console.error("LSP store init failed:", e);
      }
    }

    if (!fullIDE) {
      recordBootstrapStage("minimal-role-complete");
      return;
    }

    const pendingPluginTeardown = runtimeGlobal.__koyoriIdePluginTeardown;
    if (pendingPluginTeardown) {
      await pendingPluginTeardown;
      if (isStale()) return;
    }

    // Stage 4: Enable plugin sandbox based on the loaded setting.
    // N-29: sandbox is on by default; users can disable it in Settings.
    setSandboxMode(appState.enablePluginSandbox);
    recordBootstrapStage("plugin-sandbox");

    // Stage 4.5 (N-41 / Proposal K): Load and activate startup plugins.
    // This MUST happen before layout loading because plugins may register
    // views that the layout engine needs to render. Best-effort — errors
    // are logged to the Output panel's Plugins channel but do not block
    // subsequent bootstrap stages. Without this stage, the entire plugin
    // system stays dormant until the user manually opens PluginsView.
    try {
      await loadPlugins(() => !isStale());
      if (isStale()) return;
      await activateStartupPlugins(() => !isStale());
      if (isStale()) return;
    } catch (e: unknown) {
      if (isStale()) return;
      // loadPlugins/activateStartupPlugins already capture errors into
      // pluginStore.error; this catch is a defensive net for unexpected throws.
      console.error("Plugin bootstrap failed:", e);
    }
    recordBootstrapStage("plugins");

    // Stage 5: Load persisted layout tree (async, reads layout.json).
    // N-30: each profile has its own layout; falls back to default on error.
    await loadLayoutFromBackend(layoutService.loadLayout, () => !isStale());
    if (isStale()) return;
    recordBootstrapStage("layout");

    // Stage 6: Load workflow definitions.
    // G-SEC-03: Startup workflows are NOT auto-run on project load. They are
    // exposed via the pendingStartupWorkflows computed in the workflows store
    // so the UI can present them as "Pending Confirmation" and require the
    // user to explicitly click "Run". This prevents malicious startup
    // workflows in cloned repositories from auto-running shell commands.
    if (appState.currentProject) {
      await loadWorkflows(appState.currentProject, () => !isStale());
      if (isStale()) return;
      // prompt-4 Task 10: 项目打开后激活快照工作区根
      setSnapshotWorkspaceRoot(appState.currentProject);
    }
    recordBootstrapStage("workflows");

    // Stage 7 (prompt-4 Task 5 / prompt-5 Task A): 跨窗口事件接线已由
    // lib/crossWindowSync.ts 统一管理（ai:apply-to-editor 等映射为 store 动作）。
    // initCrossWindowSync 已在 Stage 2 调用，此处无需重复注册。
  } catch (e: unknown) {
    if (isStale()) return;
    // N-118: surface bootstrap failures to the user instead of letting
    // them vanish as unhandled rejections. The app still mounts (the UI
    // is responsive), but the user is told that startup was incomplete.
    const msg = e instanceof Error ? e.message : String(e);
    console.error("Bootstrap failed:", e);
    pushOutput("ide", "error", `Startup failed: ${msg}`);
    try {
      notifyError(`Startup failed: ${msg}`, "Bootstrap error");
    } catch {
      // notifyError may itself fail if Element Plus hasn't mounted yet;
      // the pushOutput above still records the error.
    }
  }
}

const app = createApp(App);

// Register all Element Plus icons
for (const [key, component] of Object.entries(ElementPlusIconsVue)) {
  app.component(key, component);
}

app.use(ElementPlus, {
  size: "default",
});
app.use(router);

// N-97 / Proposal AD: Global Vue error handler. Catches errors from
// component render, setup, watchers, and lifecycle hooks that would
// otherwise only appear in the browser console. We log to the Output
// panel and show a notification so the user knows something went wrong.
app.config.errorHandler = (err, _instance, info) => {
  const msg = err instanceof Error ? err.message : String(err);
  console.error("[Vue errorHandler]", err, info);
  pushOutput("ide", "error", `Vue error (${info}): ${msg}`);
  try {
    notifyError(`${msg}`, "Vue error");
  } catch {
    // notification may fail during early startup; the Output log still records it
  }
};

// N-98 / Proposal AD: window-level error and rejection handlers. These
// catch errors that escape Vue's errorHandler (e.g. errors in event
// listeners, async callbacks, or third-party scripts) and rejected
// promises that nothing else caught. Without these, the default browser
// behavior is to log to console only — users have no idea something broke.
// L-6: handler references are stored so they can be removed via
// unregisterGlobalErrorHandlers() (e.g. in tests or HMR teardown).
let globalErrorHandler: ((event: ErrorEvent) => void) | null = null;
let globalRejectionHandler: ((event: PromiseRejectionEvent) => void) | null = null;

function registerGlobalErrorHandlers(): void {
  if (typeof window === "undefined") return;
  // Guard against double-registration.
  if (globalErrorHandler && globalRejectionHandler) return;

  globalErrorHandler = (event: ErrorEvent) => {
    // event.error may be undefined for cross-origin script errors; fall
    // back to event.message.
    const err = event.error ?? event.message;
    // Silently ignore user-initiated cancellations (file picker dismiss,
    // ElMessageBox cancel, LSP context cancellation). These are not errors.
    if (isCancellationError(err)) {
      event.preventDefault();
      return;
    }
    const msg = event.message || (event.error instanceof Error ? event.error.message : "Unknown error");
    console.error("[window error]", event.error ?? event.message);
    pushOutput("ide", "error", `Uncaught error: ${msg}`);
    try {
      notifyError(msg, "Uncaught error");
    } catch {
      // notification may fail during early startup
    }
  };

  globalRejectionHandler = (event: PromiseRejectionEvent) => {
    const reason = event.reason;
    // Silently ignore user-initiated cancellations — file picker dismiss,
    // ElMessageBox cancel, LSP context cancellation. Showing a notification
    // for each is noisy and confusing.
    if (isCancellationError(reason)) {
      event.preventDefault();
      return;
    }
    const msg = reason instanceof Error ? reason.message : String(reason);
    console.error("[unhandledrejection]", reason);
    pushOutput("ide", "error", `Unhandled promise rejection: ${msg}`);
    try {
      notifyError(msg, "Unhandled rejection");
    } catch {
      // notification may fail during early startup
    }
  };

  window.addEventListener("error", globalErrorHandler);
  window.addEventListener("unhandledrejection", globalRejectionHandler);
}

/**
 * L-6: Remove the global error and unhandledrejection event listeners
 * that were registered by registerGlobalErrorHandlers(). Safe to call
 * multiple times — a no-op if the handlers were never registered or
 * already removed.
 */
export function unregisterGlobalErrorHandlers(): void {
  if (typeof window === "undefined") return;
  if (globalErrorHandler) {
    window.removeEventListener("error", globalErrorHandler);
    globalErrorHandler = null;
  }
  if (globalRejectionHandler) {
    window.removeEventListener("unhandledrejection", globalRejectionHandler);
    globalRejectionHandler = null;
  }
}

registerGlobalErrorHandlers();

export function acquireFrontendRuntime(owner: symbol): () => void {
  if (!isCurrentRuntimeOwner(owner)) return () => undefined;

  runtimeConsumerCount += 1;
  runtimeReleaseGeneration += 1;
  let released = false;

  return () => {
    if (released) return;
    released = true;
    if (!isCurrentRuntimeOwner(owner)) return;

    runtimeConsumerCount = Math.max(0, runtimeConsumerCount - 1);
    if (runtimeConsumerCount !== 0) return;

    const releaseGeneration = ++runtimeReleaseGeneration;
    queueMicrotask(() => {
      if (
        releaseGeneration !== runtimeReleaseGeneration ||
        runtimeConsumerCount !== 0 ||
        !isCurrentRuntimeOwner(owner)
      ) {
        return;
      }
      disposeFrontendRuntime(owner);
    });
  };
}

export function disposeFrontendRuntime(
  owner: symbol | null = activeRuntimeOwner,
): void {
  if (!isCurrentRuntimeOwner(owner)) return;

  runtimeGlobal.__koyoriIdeFrontendRuntimeOwner = null;
  activeRuntimeOwner = null;
  runtimeConsumerCount = 0;
  runtimeReleaseGeneration += 1;
  unregisterGlobalErrorHandlers();
  stopSystemModeListener();
  teardownPersonalization();
  // A fail-closed renderer may have no owner lifecycle to tear down, but the
  // legacy test/dev entry path can still have installed shared listeners. AI
  // and settings roles deliberately skip these main-only teardown hooks.
  const role = getFrontendRuntimeRole();
  if (isFullIDERuntimeRole(role) || role === "minimal") {
    teardownAppStateEffects();
    cleanupCodeLensProviders();
    cleanupInlayHintsProviders();
    cleanupSemanticTokensProviders();
    cleanupInlineCompletion();
    cleanupTaskStoreTimers();
    cleanupDebugRuntime();
    teardownContinuousTesting();
    resetSnapshotStore();
    clearMonacoExtensionContributes();
    cleanupPullDiagnostics();
    const pluginTeardown = deactivateAllPlugins();
    resetPluginSandboxState();
    runtimeGlobal.__koyoriIdePluginTeardown = pluginTeardown;
    void pluginTeardown.then(
      () => {
        if (runtimeGlobal.__koyoriIdePluginTeardown === pluginTeardown) {
          delete runtimeGlobal.__koyoriIdePluginTeardown;
        }
      },
      () => {
        if (runtimeGlobal.__koyoriIdePluginTeardown === pluginTeardown) {
          delete runtimeGlobal.__koyoriIdePluginTeardown;
        }
      },
    );
    cleanupWorkflowRuntime();
    stopConnectivityListener();
    void resetActivationState();
  }
  teardownCrossWindowSync();
  if (runtimeGlobal.__koyoriIdeAcquireFrontendRuntime === acquireFrontendRuntimeHook) {
    delete runtimeGlobal.__koyoriIdeAcquireFrontendRuntime;
  }
  if (runtimeGlobal.__koyoriIdeDisposeFrontendRuntime === disposeFrontendRuntimeHook) {
    delete runtimeGlobal.__koyoriIdeDisposeFrontendRuntime;
  }
}

const acquireFrontendRuntimeHook = (owner: symbol): (() => void) =>
  acquireFrontendRuntime(owner);

const disposeFrontendRuntimeHook = (owner: symbol): void => {
  disposeFrontendRuntime(owner);
};

import.meta.hot?.dispose(() => {
  disposeFrontendRuntime(activeRuntimeOwner);
});

// Mount the app immediately for a responsive first paint, then run the
// async bootstrap sequence. Settings/layout will update the UI when ready.
// N-118: bootstrap() now has its own try/catch, so `void` is safe — no
// unhandled rejection will escape.
export async function resolveFrontendRuntimeRole(): Promise<FrontendRuntimeRole> {
  const role = await resolveRuntimeRoleToken(
    (token) => WindowServiceBindings.ResolveRuntimeRole(token),
  );
  return normalizeRuntimeRole(role);
}

// Resolve the backend-issued role before mounting Vue. The bootstrap owner is
// then installed synchronously, so App.vue cannot start a privileged lifecycle
// while an AI or fail-closed renderer is still being classified.
async function startFrontendRuntime(): Promise<void> {
  const role = await resolveFrontendRuntimeRole();
  runtimeGlobal.__koyoriIdeRuntimeRole = role;
  const bootstrap = bootstrapFrontendRuntime(role);
  app.mount("#app");
  await bootstrap;
}

void startFrontendRuntime();
