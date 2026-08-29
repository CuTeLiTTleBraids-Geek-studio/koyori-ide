/**
 * G13: runtime-authoritative Extension API capability matrix.
 *
 * The EXTENSION-COMPATIBILITY.md document mirrors this table. The rule is
 * simple: an API is either really implemented (real behavior), partially
 * implemented (real but degraded, documented), or unsupported. Unsupported
 * APIs MUST fail closed with `KOYORI_IDE_EXT_API_UNSUPPORTED` (see
 * ExtensionApiUnsupportedError) — returning a default value or an empty
 * success Promise would be fake success, which G13 forbids.
 */
// Koyori IDE 模块 · Api Capability。
// 喵，这是 Koyori IDE 的 Api Capability 模块（前端实现）~

export type ApiCapabilityStatus = "implemented" | "partial" | "unsupported";

export interface ApiCapabilityEntry {
  /** Fully-qualified extension API name, e.g. "workspace.saveAll". */
  api: string;
  status: ApiCapabilityStatus;
  /** Short, honest description of the runtime behavior. */
  note: string;
}

export const EXT_API_VERSION = "v1";
export const EXT_API_UNSUPPORTED_CODE = "KOYORI_IDE_EXT_API_UNSUPPORTED";

export const apiCapabilityMatrix: ApiCapabilityEntry[] = [
  {
    api: "workspace.saveAll",
    status: "implemented",
    note: "flushes every dirty buffer through the editor bridge and propagates per-file failures (throws with failed paths); fails closed when no bridge is wired",
  },
  {
    api: "workspace.getConfiguration",
    status: "implemented",
    note: "bridged to the settings store (per-section snapshot, read-only)",
  },
  {
    api: "workspace.onDidChangeConfiguration",
    status: "implemented",
    note: "forwards settings-store changes with affectsConfiguration and refreshes the Worker snapshot",
  },
  {
    api: "workspace.createFileSystemWatcher",
    status: "implemented",
    note: "workspace-generation-scoped polling watcher reports create/change/delete using directory metadata",
  },
  {
    api: "window.showInformationMessage",
    status: "partial",
    note: "routes to host notifications when the notify bridge is wired; otherwise console-only (partial)",
  },
  {
    api: "window.showWarningMessage",
    status: "partial",
    note: "routes to host notifications when the notify bridge is wired; otherwise console-only (partial)",
  },
  {
    api: "window.showErrorMessage",
    status: "partial",
    note: "routes to host notifications when the notify bridge is wired; otherwise console-only (partial)",
  },
  {
    api: "window.showInputBox",
    status: "implemented",
    note: "opens the host input dialog; explicit cancellation resolves undefined and validation blocks invalid input",
  },
  {
    api: "window.showQuickPick",
    status: "implemented",
    note: "opens the host picker; explicit cancellation resolves undefined without choosing a default item",
  },
  {
    api: "window.setStatusBarMessage",
    status: "implemented",
    note: "renders extension-owned status text and removes it on dispose or timeout",
  },
  {
    api: "window.createStatusBarItem",
    status: "implemented",
    note: "renders a mutable extension-owned item with show/hide/dispose lifecycle",
  },
  {
    api: "window.withProgress",
    status: "implemented",
    note: "awaits the extension task while reports update the visible status progress surface",
  },
  {
    api: "window.createOutputChannel",
    status: "implemented",
    note: "routes append/clear/show/hide/dispose to the visible Output panel",
  },
  {
    api: "languages.registerDocumentRangeSemanticTokensProvider",
    status: "implemented",
    note: "bridges range semantic token providers to Monaco when the runtime supports the registration",
  },
  {
    api: "window.createTerminal",
    status: "implemented",
    note: "backed by the backend TerminalService (permission-gated)",
  },
  {
    api: "window.createWebviewPanel",
    status: "implemented",
    note: "sandboxed iframe with CSP nonce (permission-gated)",
  },
];

/** Returns the matrix entry for an API name, or undefined when not tracked. */
export function capabilityOf(api: string): ApiCapabilityEntry | undefined {
  return apiCapabilityMatrix.find((entry) => entry.api === api);
}