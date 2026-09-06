/**
 * Extension security store (G-VSC-03 / G-SEC-12) — frontend orchestration
 * for VS Code extension security gates.
 *
 * This store is the frontend counterpart to the Go ExtensionSecurityService.
 * It:
 *   - Manages extension security states (classification, SHA-256 integrity,
 *     enabled, blacklist, pending-review).
 *   - Calls the backend to classify, integrity-check, enable/disable extensions.
 *   - Shows the permission dialog (ExtensionPermissionDialog) when a user
 *     tries to enable a Reviewed or Restricted extension.
 *   - Blocks access to `appState.aiApiKey` from extension contexts
 *     (G-SEC-12 requirement 5: resource isolation).
 *
 * The store is intentionally separate from the native plugin store
 * (stores/plugins.ts) because VS Code extensions use a richer permission
 * model and a separate install path (MarketplaceService).
 */
// Koyori IDE 模块 · Extension Security。
// 喵，这是 Koyori IDE 的 Extension Security 模块（前端实现）~

import { reactive, computed, ref } from "vue";
import { errorMessage } from "@/lib/errors";
import { appState } from "@/stores/app";

// ---------------------------------------------------------------------------
// Types — mirror the Go ExtensionSecurityInfo / ExtensionSecurityLevel /
// ExtensionPermission structs (services/extension_security_service.go).
// ---------------------------------------------------------------------------

export type ExtensionSecurityLevel = "trusted" | "reviewed" | "restricted";

export type ExtensionPermission =
  | "fs.read"
  | "fs.write"
  | "shell.execute"
  | "network"
  | "clipboard"
  | "ui.notifications"
  | "ui.webview"
  // F-6 (task-3.md): 扩展宿主 API 表面补齐新增权限
  | "tasks.execute"
  | "debug.execute"
  | "scm.read"
  | "scm.write"
  | "env.openExternal"
  | "secrets.read"
  | "secrets.write";

export interface ExtensionSecurityInfo {
  extensionId: string;
  level: ExtensionSecurityLevel;
  permissions: ExtensionPermission[];
  sha256: string;
  /** True only after the VSIX matched its expected SHA-256 digest. */
  integrityChecked: boolean;
  /** Deprecated compatibility alias for integrityChecked; do not use as a trust signal. */
  verified: boolean;
  enabled: boolean;
  blacklisted: boolean;
  pendingReview: boolean;
}

// ---------------------------------------------------------------------------
// Permission metadata — human-readable descriptions and risk tiers used by
// the permission dialog.
// ---------------------------------------------------------------------------

const PERMISSION_DESCRIPTIONS: Record<ExtensionPermission, string> = {
  "fs.read":
    "Read files in your workspace.",
  "fs.write":
    "Create, modify, or delete files in your workspace.",
  "shell.execute":
    "Execute shell commands on your machine.",
  network:
    "Make outbound network requests to any destination.",
  clipboard:
    "Read from and write to the system clipboard.",
  "ui.notifications":
    "Show notification messages in the IDE.",
  "ui.webview":
    "Render web content (HTML/CSS/JS) in a sandboxed panel.",
  // F-6 (task-3.md): 新增权限的描述
  "tasks.execute":
    "Run build, test, and other tasks defined in the workspace.",
  "debug.execute":
    "Start debug sessions (DAP) for the workspace.",
  "scm.read":
    "Read source-control state (git status, diff, branches).",
  "scm.write":
    "Stage, unstage, and commit source-control changes.",
  "env.openExternal":
    "Open external URLs or files in the system default application.",
  "secrets.read":
    "Read secrets stored in the OS keychain/credential store.",
  "secrets.write":
    "Store and delete secrets in the OS keychain/credential store.",
};

const PERMISSION_RISK: Record<ExtensionPermission, "low" | "medium" | "high"> = {
  "fs.read": "low",
  "ui.notifications": "low",
  "ui.webview": "low",
  clipboard: "medium",
  "fs.write": "medium",
  "shell.execute": "high",
  network: "high",
  // F-6 (task-3.md): 新增权限的风险等级
  "scm.read": "low",
  "secrets.read": "low",
  "tasks.execute": "medium",
  "debug.execute": "medium",
  "scm.write": "medium",
  "secrets.write": "medium",
  "env.openExternal": "high",
};

/**
 * Human-readable description of a permission, shown in the approval dialog.
 */
export function permissionDescription(perm: ExtensionPermission): string {
  return PERMISSION_DESCRIPTIONS[perm] ?? "Unknown permission.";
}

/**
 * Risk tier for a permission: "low" (read-only), "medium" (write/clipboard),
 * "high" (shell/network). Used by the dialog to sort and color-code the list.
 */
export function permissionRisk(perm: ExtensionPermission): "low" | "medium" | "high" {
  return PERMISSION_RISK[perm] ?? "medium";
}

// ---------------------------------------------------------------------------
// Store state
// ---------------------------------------------------------------------------

interface ExtensionSecurityStoreState {
  /** All known extension security infos, keyed by extensionId. */
  infos: Record<string, ExtensionSecurityInfo>;
  loading: boolean;
  error: string | null;
}

export const extensionSecurityStore = reactive<ExtensionSecurityStoreState>({
  infos: {},
  loading: false,
  error: null,
});

export const extensionSecurityInfos = computed(() =>
  Object.values(extensionSecurityStore.infos),
);
export const isLoadingExtensionSecurity = computed(
  () => extensionSecurityStore.loading,
);
export const extensionSecurityError = computed(
  () => extensionSecurityStore.error,
);

// ---------------------------------------------------------------------------
// Permission dialog state
//
// The dialog is shown when the user attempts to enable a Reviewed or
// Restricted extension. The store exposes a reactive `pendingApproval`
// ref that the host component (App.vue or PluginsView) binds to the
// ExtensionPermissionDialog's `visible` + `info` props.
// ---------------------------------------------------------------------------

export const pendingApproval = ref<ExtensionSecurityInfo | null>(null);

/**
 * Show the permission dialog for an extension. Called internally by
 * `requestEnableExtension` when the backend reports the extension is
 * Reviewed/Restricted or pending review. The host component renders
 * <ExtensionPermissionDialog :visible="!!pendingApproval" :info="pendingApproval" />
 * and listens for @approve / @close.
 */
export function showPermissionDialog(info: ExtensionSecurityInfo): void {
  pendingApproval.value = info;
}

/**
 * Dismiss the permission dialog without enabling.
 */
export function dismissPermissionDialog(): void {
  pendingApproval.value = null;
}

// ---------------------------------------------------------------------------
// Backend integration
//
// The actual Wails bindings for ExtensionSecurityService are generated at
// build time. To avoid a hard dependency on bindings that may not yet exist
// in the repo (the service is registered in main.go but the bindings are
// regenerated by the Wails Vite plugin), we use a thin RPC shim that calls
// the generated bindings lazily. Tests inject a mock backend.
// ---------------------------------------------------------------------------

/**
 * Backend adapter interface. The default implementation calls the Wails
 * bindings; tests inject a mock.
 */
export interface ExtensionSecurityBackend {
  classifyExtension(permissions: ExtensionPermission[]): Promise<ExtensionSecurityLevel>;
  registerInstall(
    extensionId: string,
    permissions: ExtensionPermission[],
    vsixPath: string,
    expectedSHA256: string,
  ): Promise<ExtensionSecurityInfo>;
  getSecurityInfo(extensionId: string): Promise<ExtensionSecurityInfo>;
  setExtensionEnabled(
    extensionId: string,
    enabled: boolean,
    explicitApproval?: boolean,
  ): Promise<void>;
  listSecurityInfo(): Promise<ExtensionSecurityInfo[]>;
  isBlacklisted(publisher: string, name: string): Promise<boolean>;
  addToBlacklist(publisher: string, name: string): Promise<void>;
  canInstall(publisher: string, name: string): Promise<void>;
}

// Lazy-loaded default backend that calls the Wails bindings.
let backend: ExtensionSecurityBackend | null = null;

/**
 * Inject the backend adapter. Tests call this with a mock; the app calls
 * it once on startup with the default Wails-backed adapter.
 */
export function setExtensionSecurityBackend(b: ExtensionSecurityBackend | null): void {
  backend = b;
}

/**
 * Cache for the lazily-loaded bindings module. Typed as a minimal shape
 * so the default backend can call the methods without a hard type
 * dependency on the generated file.
 */
interface ExtensionSecurityBindingsShape {
  ClassifyExtension(permissions: ExtensionPermission[]): Promise<string>;
  RegisterInstall(
    extensionId: string,
    permissions: ExtensionPermission[],
    vsixPath: string,
    expectedSHA256: string,
  ): Promise<ExtensionSecurityInfo>;
  GetSecurityInfo(extensionId: string): Promise<ExtensionSecurityInfo>;
  SetExtensionEnabled(
    extensionId: string,
    enabled: boolean,
    explicitApproval: boolean,
  ): Promise<void>;
  ListSecurityInfo(): Promise<ExtensionSecurityInfo[]>;
  IsBlacklisted(publisher: string, name: string): Promise<boolean>;
  AddToBlacklist(publisher: string, name: string): Promise<void>;
  CanInstall(publisher: string, name: string): Promise<void>;
}

let bindingsCache: ExtensionSecurityBindingsShape | null = null;

async function loadBindings(): Promise<ExtensionSecurityBindingsShape> {
  if (bindingsCache) return bindingsCache;
  // 使用字面量路径（无 @vite-ignore），让 vite 将 bindings 打包为 chunk。
  const mod = await import("../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/extensionsecurityservice.js");
  // bindings 文件使用命名导出，直接将 mod 作为 ExtensionSecurityBindingsShape 使用。
  bindingsCache = mod as unknown as ExtensionSecurityBindingsShape;
  return bindingsCache;
}

/**
 * Default backend that lazily imports the Wails-generated bindings. If the
 * bindings are not available (e.g. in unit tests), calls throw — tests
 * should inject a mock via setExtensionSecurityBackend before exercising
 * the store.
 */
function getDefaultBackend(): ExtensionSecurityBackend {
  return {
    async classifyExtension(permissions) {
      const b = await loadBindings();
      const level = (await b.ClassifyExtension(permissions)) as string;
      return level as ExtensionSecurityLevel;
    },
    async registerInstall(extensionId, permissions, vsixPath, expectedSHA256) {
      const b = await loadBindings();
      return (await b.RegisterInstall(
        extensionId,
        permissions,
        vsixPath,
        expectedSHA256,
      )) as ExtensionSecurityInfo;
    },
    async getSecurityInfo(extensionId) {
      const b = await loadBindings();
      return (await b.GetSecurityInfo(extensionId)) as ExtensionSecurityInfo;
    },
    async setExtensionEnabled(extensionId, enabled, explicitApproval) {
      const b = await loadBindings();
      await b.SetExtensionEnabled(extensionId, enabled, explicitApproval ?? false);
    },
    async listSecurityInfo() {
      const b = await loadBindings();
      return (await b.ListSecurityInfo()) as ExtensionSecurityInfo[];
    },
    async isBlacklisted(publisher, name) {
      const b = await loadBindings();
      return (await b.IsBlacklisted(publisher, name)) as boolean;
    },
    async addToBlacklist(publisher, name) {
      const b = await loadBindings();
      await b.AddToBlacklist(publisher, name);
    },
    async canInstall(publisher, name) {
      const b = await loadBindings();
      await b.CanInstall(publisher, name);
    },
  };
}

function getBackend(): ExtensionSecurityBackend {
  if (backend) return backend;
  backend = getDefaultBackend();
  return backend;
}

// ---------------------------------------------------------------------------
// Store actions
// ---------------------------------------------------------------------------

/**
 * Load all extension security infos from the backend and sync the local
 * store. Safe to call repeatedly.
 */
export async function loadExtensionSecurityInfos(): Promise<void> {
  extensionSecurityStore.loading = true;
  extensionSecurityStore.error = null;
  try {
    const list = await getBackend().listSecurityInfo();
    extensionSecurityStore.infos = {};
    for (const info of list) {
      extensionSecurityStore.infos[info.extensionId] = info;
    }
  } catch (e: unknown) {
    extensionSecurityStore.error = errorMessage(e);
  } finally {
    extensionSecurityStore.loading = false;
  }
}

/**
 * Get the security info for a single extension. Returns undefined if the
 * extension has no recorded security state.
 */
export function getExtensionSecurityInfo(
  extensionId: string,
): ExtensionSecurityInfo | undefined {
  return extensionSecurityStore.infos[extensionId];
}

/** Refresh one security record without replacing unrelated cached records. */
export async function refreshExtensionSecurityInfo(
  extensionId: string,
): Promise<void> {
  const info = await getBackend().getSecurityInfo(extensionId);
  extensionSecurityStore.infos[extensionId] = info;
}

/** Remove one local security record after the backend uninstall commits. */
export function removeExtensionSecurityInfo(extensionId: string): void {
  delete extensionSecurityStore.infos[extensionId];
  if (pendingApproval.value?.extensionId === extensionId) {
    pendingApproval.value = null;
  }
}

/**
 * Request to enable an extension. This is the main entry point for the
 * G-VSC-03 / G-SEC-12 permission gate:
 *
 * 1. Fetch the extension's security info from the backend.
 * 2. If the extension is blacklisted → reject with an error.
 * 3. If the extension has not passed its SHA-256 integrity check → reject.
 * 4. If the extension is Restricted OR pending review → show the
 *    permission dialog. The actual enable happens in
 *    `confirmEnableExtension` after the user approves.
 * 5. Otherwise (Trusted/Reviewed, already reviewed) → enable directly.
 *
 * Returns true if the extension was enabled (or the dialog was shown for
 * user approval), false if the request was rejected.
 */
export async function requestEnableExtension(
  extensionId: string,
  gateOnly = false,
): Promise<boolean> {
  extensionSecurityStore.error = null;
  let info: ExtensionSecurityInfo;
  try {
    info = await getBackend().getSecurityInfo(extensionId);
  } catch (e: unknown) {
    extensionSecurityStore.error = errorMessage(e);
    return false;
  }

  // Blacklist gate — never show the dialog for blacklisted extensions.
  if (info.blacklisted) {
    extensionSecurityStore.error =
      "Extension is on the known-malicious blacklist and cannot be enabled.";
    return false;
  }

  // Integrity gate: the compatibility `verified` field must never bypass the
  // explicit SHA-256 integrity result. Missing integrityChecked is fail-closed.
  if (!info.integrityChecked) {
    extensionSecurityStore.infos[extensionId] = info;
    extensionSecurityStore.error =
      "Extension has not passed the SHA-256 integrity check and cannot be enabled.";
    return false;
  }

  // Permission gate — Restricted or pending-review extensions require
  // explicit user approval via the permission dialog. Trusted and Reviewed
  // extensions whose integrity was checked can otherwise enable directly.
  if (info.level === "restricted" || info.pendingReview) {
    // Show the permission dialog. The host component renders
    // ExtensionPermissionDialog bound to pendingApproval.
    extensionSecurityStore.infos[extensionId] = info;
    showPermissionDialog(info);
    return false; // Not yet enabled — waiting for user approval.
  }

  // Trusted/Reviewed extension → enable directly.
  if (gateOnly) return true;
  return confirmEnableExtension(extensionId, false);
}

/**
 * Confirm enabling an extension after the user approves the permission
 * dialog (or directly for Trusted extensions). `explicitApproval` must be
 * true for Restricted extensions (the backend enforces this).
 *
 * Returns true on success, false on failure (error is stored in
 * extensionSecurityStore.error).
 */
export async function confirmEnableExtension(
  extensionId: string,
  explicitApproval: boolean,
): Promise<boolean> {
  extensionSecurityStore.error = null;
  try {
    await getBackend().setExtensionEnabled(
      extensionId,
      true,
      explicitApproval,
    );
    // Refresh the info so the UI reflects the new enabled state.
    const info = await getBackend().getSecurityInfo(extensionId);
    extensionSecurityStore.infos[extensionId] = info;
    return true;
  } catch (e: unknown) {
    extensionSecurityStore.error = errorMessage(e);
    return false;
  }
}

/**
 * Disable an extension. Always succeeds for non-blacklisted extensions.
 */
export async function disableExtension(extensionId: string): Promise<boolean> {
  extensionSecurityStore.error = null;
  try {
    await getBackend().setExtensionEnabled(extensionId, false);
    const info = await getBackend().getSecurityInfo(extensionId);
    extensionSecurityStore.infos[extensionId] = info;
    return true;
  } catch (e: unknown) {
    extensionSecurityStore.error = errorMessage(e);
    return false;
  }
}

/**
 * Handle the dialog's "approve" event: enable the extension with explicit
 * approval. Dismisses the dialog afterwards.
 */
export async function handleApprove(extensionId: string): Promise<void> {
  const info = pendingApproval.value;
  const explicitApproval = info?.level === "restricted";
  dismissPermissionDialog();
  await confirmEnableExtension(extensionId, explicitApproval);
}

/**
 * Check if an extension can be installed (blacklist pre-check). Returns
 * true if installation may proceed, false if the extension is blacklisted.
 */
export async function checkCanInstall(
  publisher: string,
  name: string,
): Promise<boolean> {
  try {
    await getBackend().canInstall(publisher, name);
    return true;
  } catch {
    return false;
  }
}

// ---------------------------------------------------------------------------
// G-SEC-12 requirement 5: Resource isolation — block access to
// appState.aiApiKey from extension contexts.
//
// Extensions run in an isolated host (Web Worker sandbox or sandboxed
// iframe) and must never access the main webview's appState, which holds
// the AI API key. This guard is a defense-in-depth measure: even if an
// extension somehow obtains a reference to appState, this function (used
// by the API surface compatibility layer) refuses to return the key.
// ---------------------------------------------------------------------------

/**
 * Marker symbol attached to objects that are exposed to extension
 * contexts. The API surface checks for this before returning any value
 * that could leak appState.
 */
export const EXTENSION_CONTEXT_MARKER = Symbol("koyori-ide.extension.context");

/**
 * Returns true if the current execution context is an extension sandbox
 * (Worker or sandboxed iframe). The API surface uses this to enforce the
 * aiApiKey access block.
 *
 * Detection: extension contexts are created by the extensionHost, which
 * sets a global flag. In the main webview this is always false.
 *
 * C-7: 多重校验 — 即使 sandbox="allow-same-origin" 让顶层 iframe 绕过
 * `window.parent !== window` 检测（同源时 parent 与 Window 同对象），仍需通过：
 *   1. origin 白名单（只允许 Wails 主 webview 的固定 origin）
 *   2. CSP nonce（main.go injectNonceIntoHTML 注入到所有 <script nonce="...">）
 * 任一不通过即视为扩展上下文。这是 defense-in-depth，不依赖单一浏览器 API。
 */
// C-7: Wails 主 webview 的固定 origin 白名单。
// - wails://localhost（Windows / Linux 主 webview）
// - http://wails.localhost（macOS 主 webview）
// - https://wails.localhost（Wails HTTPS 模式）
// - Vite dev server origin（开发环境，主 webview 在 dev 下经 Vite 提供）
const ALLOWED_MAIN_WEBVIEW_ORIGINS = new Set<string>([
  "wails://localhost",
  "http://wails.localhost",
  "https://wails.localhost",
  // Vite dev server（开发环境主 webview）
  "http://localhost:5173",
  "http://localhost:34115",
  "http://127.0.0.1:5173",
  "http://127.0.0.1:34115",
]);

/**
 * C-7: 检查当前 origin 是否在主 webview 白名单内。
 * 不在白名单的 origin 一律视为扩展上下文（防 sandbox="allow-same-origin" 绕过）。
 */
export interface ExtensionContextSignals {
  worker: boolean;
  childFrame: boolean;
  marker: boolean;
  hasWindow: boolean;
  origin: string | null;
  nonce: string | null;
}

export function classifyExtensionContext(signals: ExtensionContextSignals): boolean {
  if (signals.worker || signals.childFrame || signals.marker) return true;
  if (!signals.hasWindow) return false;
  return !signals.origin
    || !ALLOWED_MAIN_WEBVIEW_ORIGINS.has(signals.origin)
    || !signals.nonce
    || signals.nonce.length < 16;
}

export function isExtensionContext(): boolean {
  const hasWindow = typeof window !== "undefined";
  let childFrame = false;
  let origin: string | null = null;
  let nonce: string | null = null;
  if (hasWindow) {
    try {
      childFrame = window.parent !== window;
      origin = window.location?.origin ?? null;
      const scripts = document.querySelectorAll("script[nonce]");
      for (let i = 0; i < scripts.length; i++) {
        const candidate = scripts[i].getAttribute("nonce");
        if (candidate && candidate.length >= 16) {
          nonce = candidate;
          break;
        }
      }
    } catch {
      childFrame = true;
    }
  }
  const marker = (globalThis as { __KOYORI_IDE_EXTENSION_CONTEXT__?: boolean })
    .__KOYORI_IDE_EXTENSION_CONTEXT__ === true;
  return classifyExtensionContext({
    worker: typeof self !== "undefined" && !hasWindow,
    childFrame,
    marker,
    hasWindow,
    origin,
    nonce,
  });
}

/**
 * Get the AI API key. Returns an empty string when called from an
 * extension context (G-SEC-12 requirement 5). This is the single choke
 * point the API surface uses — extensions that try to read the key via
 * any compatibility-layer API hit this guard.
 *
 * Defense-in-depth: even if appState.aiApiKey is non-empty in the main
 * webview, this function returns "" for extension contexts.
 */
export function getAiApiKeyForContext(): string {
  if (isExtensionContext()) {
    // G-SEC-12 req. 5: extensions cannot access appState.aiApiKey.
    return "";
  }
  return appState.aiApiKey ?? "";
}

/**
 * Assert that the current context is NOT an extension context. Used by
 * API surface methods that must never be callable from extensions (e.g.
 * reading raw secrets). Throws if called from an extension context.
 */
export function assertNotExtensionContext(operation: string): void {
  if (isExtensionContext()) {
    throw new Error(
      `Operation "${operation}" is blocked in extension contexts (G-SEC-12 resource isolation).`,
    );
  }
}

/**
 * Reset the store state. Used in tests.
 */
export function resetExtensionSecurityStore(): void {
  extensionSecurityStore.infos = {};
  extensionSecurityStore.loading = false;
  extensionSecurityStore.error = null;
  pendingApproval.value = null;
  backend = null;
}
