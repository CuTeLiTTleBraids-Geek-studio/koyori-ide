/**
 * G-VSC-02: Lightweight VS Code Extension Host.
 *
 * The Extension Host loads VS Code extensions, hands each one a subset of
 * the `vscode` API (see vscodeApi.ts), and bridges the extension's
 * registrations to koyori-ide services:
 *
 *   - vscode.languages.register*Provider → Monaco language providers
 *   - vscode.workspace.fs                → backend FileService (permission-gated,
 *                                           pathsec-validated on the backend)
 *   - vscode.window.createWebviewPanel   → sandboxed iframe (G-SEC-05)
 *   - vscode.commands.registerCommand    → host command registry (disposable)
 *   - vscode.commands.executeCommand     → registry + dangerous-cmd gate (G-SEC-12)
 *
 * Security model (G-SEC-12):
 *   - Each extension is classified Trusted / Reviewed / Restricted from its
 *     declared permissions (permissions.ts).
 *   - Restricted extensions are disabled by default and require explicit
 *     user approval before activation (approveExtension).
 *   - Privileged runtime operations (fs.read, fs.write) check the
 *     extension's declared permission before dispatching.
 *   - Dangerous commands (terminal sendSequence, _workbench.*) require a
 *     confirmation callback; default-deny when no callback is configured.
 */
// Koyori IDE 模块 · Extension Host。
// 喵，这是 Koyori IDE 的 Extension Host 模块（前端实现）~

import {
  classifyExtension,
  hasPermission,
  registerExtensionPermissions,
  requireDeclaredPermissions,
  unregisterExtensionPermissions,
  type ExtensionPermission,
  type SecurityLevel,
} from "@/lib/extensionHost/permissions";
import {
  createVscodeAPI,
  type DebugConfiguration,
  type DebugConfigurationProvider,
  type Disposable,
  type DocumentSelector,
  type Event,
  FileType,
  type FindTextInFilesOptions,
  type GlobPattern,
  type InputBoxOptions,
  type LanguageProviderKind,
  type OutputChannel,
  type QuickPickItem,
  type QuickPickOptions,
  type SourceControl,
  type SourceControlInputBox,
  type SourceControlResourceGroup,
  type SourceControlResourceState,
  type Task,
  type TaskExecution,
  type TaskProvider,
  type Terminal,
  type TerminalOptions,
  type TextDocument,
  type TextDocumentChangeEvent,
  type TextEditor,
  type TextSearchQuery,
  type TextSearchResult,
  type TreeDataProvider,
  type Uri,
  type VscodeAPI,
  type VscodeHostBridge,
  type Webview,
  type WebviewPanel,
  type WebviewViewProvider,
  type WorkspaceFolder,
} from "@/lib/extensionHost/vscodeApi";
import DOMPurify from "dompurify";
import * as TaskServiceBindings from "../../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/taskservice.js";

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

/**
 * Descriptor for an extension to activate. Mirrors the fields the host
 * needs from the extension's `package.json` (id, main entry, permissions).
 */
export interface ExtensionDescriptor {
  id: string;
  mainPath: string;
  permissions?: ExtensionPermission[];
}

/**
 * The shape of an extension's main module. `activate` receives the vscode
 * API shim; `deactivate` is optional and called on shutdown/disable.
 */
export interface ExtensionModule {
  activate(api: VscodeAPI): void | Promise<void>;
  deactivate?(): void | Promise<void>;
}

/** Factory that loads an extension's main module. Injectable for tests. */
export type ExtensionModuleLoader = (
  extensionId: string,
  mainPath: string,
) => Promise<ExtensionModule>;

/** Confirmation callback for dangerous commands (G-SEC-12). */
export type ConfirmHandler = (
  command: string,
  args: unknown[],
) => Promise<boolean>;

/** A minimal Monaco namespace subset used for language-provider bridging. */
export interface MonacoBridge {
  languages: {
    registerCompletionItemProvider(
      language: string,
      provider: unknown,
    ): Disposable;
    registerHoverProvider(language: string, provider: unknown): Disposable;
    registerDefinitionProvider?(language: string, provider: unknown): Disposable;
    registerCodeActionProvider?(language: string, provider: unknown): Disposable;
    // F-6 (task-3.md): 17 additional Monaco language provider registrations.
    // All optional — when Monaco lacks a method, the host falls back to a
    // no-op disposable (provider is still tracked for cleanup).
    registerReferenceProvider?(language: string, provider: unknown): Disposable;
    registerCodeLensProvider?(language: string, provider: unknown): Disposable;
    registerDocumentFormattingEditProvider?(
      language: string,
      provider: unknown,
    ): Disposable;
    registerDocumentRangeFormattingEditProvider?(
      language: string,
      provider: unknown,
    ): Disposable;
    registerOnTypeFormattingEditProvider?(
      language: string,
      provider: unknown,
      firstTriggerCharacter: string,
      moreTriggerCharacter?: string[],
    ): Disposable;
    registerSignatureHelpProvider?(
      language: string,
      provider: unknown,
    ): Disposable;
    registerWorkspaceSymbolProvider?(
      provider: unknown,
    ): Disposable;
    registerDocumentLinkProvider?(
      language: string,
      provider: unknown,
    ): Disposable;
    registerColorProvider?(language: string, provider: unknown): Disposable;
    registerFoldingRangeProvider?(
      language: string,
      provider: unknown,
    ): Disposable;
    registerDeclarationProvider?(
      language: string,
      provider: unknown,
    ): Disposable;
    registerImplementationProvider?(
      language: string,
      provider: unknown,
    ): Disposable;
    registerTypeDefinitionProvider?(
      language: string,
      provider: unknown,
    ): Disposable;
    registerRenameProvider?(language: string, provider: unknown): Disposable;
    registerDocumentSymbolProvider?(
      language: string,
      provider: unknown,
    ): Disposable;
    registerDocumentSemanticTokensProvider?(
      language: string,
      provider: unknown,
    ): Disposable;
    registerDocumentHighlightProvider?(
      language: string,
      provider: unknown,
    ): Disposable;
    registerInlayHintsProvider?(
      language: string,
      provider: unknown,
    ): Disposable;
  };
}

/**
 * G13: versioned, machine-readable error for extension APIs that are not
 * implemented. Extensions must never receive a fake success for an API we
 * cannot honor — they get this error instead. The code is stable so callers
 * can branch on it, and the api field names the exact surface.
 */
export class ExtensionApiUnsupportedError extends Error {
  readonly code = "KOYORI_IDE_EXT_API_UNSUPPORTED";
  readonly apiVersion = "v1";
  readonly api: string;
  constructor(api: string, detail?: string) {
    super(
      `KOYORI_IDE_EXT_API_UNSUPPORTED: ${api} is not implemented (koyori-ide extension API v1)` +
        (detail ? `: ${detail}` : ""),
    );
    this.name = "ExtensionApiUnsupportedError";
    this.api = api;
  }
}

/** Options for constructing an ExtensionHost. */
export interface ExtensionHostOptions {
  /** Loader for extension main modules. Required for activate(). */
  loadModule?: ExtensionModuleLoader;
  /** Confirmation callback for dangerous commands. Default-deny if unset. */
  confirmHandler?: ConfirmHandler;
  /** Monaco namespace for language-provider bridging. Optional. */
  monaco?: MonacoBridge;
  /**
   * BUG-FIX-2d: 回调获取当前活跃编辑器状态。
   * 由宿主 Vue App 注入，连接 editor store 与扩展宿主。
   * 返回 undefined 表示无活跃编辑器。
   */
  onGetActiveTextEditor?: () => TextEditor | undefined;
  /**
   * BUG-FIX-2d: 回调获取扩展配置。
   * 由宿主 Vue App 注入，连接 settings store 与扩展宿主。
   * section 参数对应 vscode.workspace.getConfiguration(section) 的 section 参数。
   */
  onGetConfiguration?: (section?: string) => Record<string, unknown>;
  /**
   * G13: real save-all bridge. Injected by the host app; wired to the editor
   * store so workspace.saveAll actually flushes dirty buffers and propagates
   * per-file failures. When unset, saveAll fails closed (no fake success).
   */
  onSaveAll?: (
    includeUntitled?: boolean,
  ) => Promise<{ savedCount: number; failedPaths: string[] }>;
  /**
   * G13: real notification surface. Injected by the host app (lib/notifications).
   * When unset, notifications degrade to console logging (Partial, never fake
   * UI success).
   */
  onNotify?: (level: "info" | "warn" | "error", message: string) => void;
}

// ---------------------------------------------------------------------------
// Internal registry types
// ---------------------------------------------------------------------------

interface RegisteredCommand {
  extensionId: string;
  handler: (...args: unknown[]) => unknown | Promise<unknown>;
}

interface ExtensionEntry {
  descriptor: ExtensionDescriptor;
  module: ExtensionModule;
  disposables: Disposable[];
  securityLevel: SecurityLevel;
}

// ---------------------------------------------------------------------------
// Dangerous command detection (G-SEC-12)
// ---------------------------------------------------------------------------

/**
 * Commands that can send arbitrary input to the terminal or invoke internal
 * workbench machinery. Executing these requires explicit user confirmation
 * because a malicious extension could use them to run arbitrary shell
 * commands without declaring the `shell.execute` permission.
 */
const DANGEROUS_COMMANDS = new Set<string>([
  "workbench.action.terminal.sendSequence",
]);

function isDangerousCommand(command: string): boolean {
  if (DANGEROUS_COMMANDS.has(command)) return true;
  // Internal workbench commands (prefixed with `_workbench.`) are not part
  // of the public API and can trigger privileged host actions.
  if (command.startsWith("_workbench.")) return true;
  return false;
}

const EXTENSION_SECRET_STORAGE_UNAVAILABLE =
  "ERR_EXTENSION_SECRET_STORAGE_UNAVAILABLE: secure extension secret storage requires a trusted backend host";

function bytesToHex(bytes: Uint8Array): string {
  return Array.from(bytes, (value) => value.toString(16).padStart(2, "0")).join("");
}

interface ExtensionHostModuleState {
  webviewIframeDisposers: WeakMap<HTMLIFrameElement, () => void>;
  hosts: Set<WeakRef<ExtensionHost>>;
}

const extensionHostModuleState: ExtensionHostModuleState = {
  webviewIframeDisposers: new WeakMap<HTMLIFrameElement, () => void>(),
  hosts: new Set<WeakRef<ExtensionHost>>(),
};

export async function resetExtensionHostModuleState(): Promise<void> {
  const hosts: ExtensionHost[] = [];
  for (const hostRef of extensionHostModuleState.hosts) {
    const host = hostRef.deref();
    if (host) {
      hosts.push(host);
    } else {
      extensionHostModuleState.hosts.delete(hostRef);
    }
  }
  await Promise.allSettled(
    hosts.map((host) => host.disposeAll()),
  );
  extensionHostModuleState.webviewIframeDisposers =
    new WeakMap<HTMLIFrameElement, () => void>();
}

import.meta.hot?.dispose(() => {
  void resetExtensionHostModuleState();
});

function decodeWorkspacePath(value: string): string {
  let decoded = value;
  for (let pass = 0; pass < 4 && /%[0-9a-f]{2}/i.test(decoded); pass++) {
    try {
      const next = decodeURIComponent(decoded);
      if (next === decoded) break;
      decoded = next;
    } catch {
      throw new Error(`Path traversal denied: invalid encoded path "${value}"`);
    }
  }
  if (/%[0-9a-f]{2}/i.test(decoded) || decoded.includes("\0")) {
    throw new Error(`Path traversal denied: invalid encoded path "${value}"`);
  }
  return decoded.replace(/\\/g, "/");
}

function isAbsoluteWorkspacePath(value: string): boolean {
  return value.startsWith("/") || /^[A-Za-z]:\//.test(value);
}

function normalizeWorkspacePath(value: string): string {
  const unified = value.replace(/\\/g, "/");
  let prefix = "";
  let remainder = unified;
  if (/^[A-Za-z]:\//.test(unified)) {
    prefix = unified.slice(0, 2);
    remainder = unified.slice(3);
  } else if (unified.startsWith("//")) {
    prefix = "//";
    remainder = unified.slice(2);
  } else if (unified.startsWith("/")) {
    prefix = "/";
    remainder = unified.slice(1);
  }

  const segments: string[] = [];
  for (const segment of remainder.split("/")) {
    if (segment === "" || segment === ".") continue;
    if (segment === "..") {
      if (segments.length > 0) {
        segments.pop();
      } else if (!prefix) {
        segments.push(segment);
      }
      continue;
    }
    segments.push(segment);
  }

  if (prefix === "/" || prefix === "//") {
    return `${prefix}${segments.join("/")}`;
  }
  if (prefix) {
    return `${prefix}/${segments.join("/")}`;
  }
  return segments.join("/");
}

function createWebviewNonce(): string {
  const webCrypto = globalThis.crypto;
  if (!webCrypto?.getRandomValues) {
    throw new Error("Secure random generation is unavailable for webview CSP nonce");
  }
  return bytesToHex(webCrypto.getRandomValues(new Uint8Array(16)));
}

function webviewScriptsEnabled(options: unknown): boolean {
  return typeof options === "object"
    && options !== null
    && Reflect.get(options, "enableScripts") === true;
}

function webviewContentSecurityPolicy(
  nonce: string,
  allowScripts: boolean,
  allowNetwork: boolean,
): string {
  const resourceSources = allowNetwork ? "data: https:" : "data:";
  return [
    "default-src 'none'",
    `img-src ${resourceSources}`,
    allowScripts ? `script-src 'nonce-${nonce}'` : "script-src 'none'",
    "script-src-attr 'none'",
    `style-src 'nonce-${nonce}'`,
    "style-src-attr 'none'",
    `font-src ${resourceSources}`,
    `media-src ${resourceSources}`,
    allowNetwork ? "connect-src https:" : "connect-src 'none'",
    "navigate-to 'none'",
    "frame-src 'none'",
    "object-src 'none'",
    "base-uri 'none'",
    "form-action 'none'",
  ].join("; ");
}

function createSecureWebviewSrcdoc(
  value: string,
  allowScripts: boolean,
  allowNetwork: boolean,
): string {
  const nonce = createWebviewNonce();
  const inlineScripts = allowScripts
    ? Array.from(
        new DOMParser().parseFromString(value, "text/html").querySelectorAll("script"),
      )
        .filter((element) => !element.hasAttribute("src"))
        .map((element) => ({
          source: element.textContent ?? "",
          type: element.getAttribute("type") ?? "",
        }))
        .filter(({ type }) =>
          type === "" ||
          type === "module" ||
          type === "text/javascript" ||
          type === "application/javascript",
        )
    : [];
  const sanitized = DOMPurify.sanitize(value, {
    WHOLE_DOCUMENT: true,
    FORBID_TAGS: ["script", "iframe", "object", "embed", "base", "form", "meta"],
    FORBID_ATTR: ["srcdoc", "style"],
  });
  const parsed = new DOMParser().parseFromString(sanitized, "text/html");
  parsed
    .querySelectorAll('meta[http-equiv="Content-Security-Policy" i]')
    .forEach((element) => element.remove());
  parsed.querySelectorAll("[style]").forEach((element) => {
    element.removeAttribute("style");
  });
  if (!allowNetwork) {
    parsed.querySelectorAll("[href], [src], [ping], [poster]").forEach((element) => {
      for (const attribute of ["href", "src", "ping", "poster"] as const) {
        const target = element.getAttribute(attribute)?.trim() ?? "";
        if (
          target
          && !target.startsWith("#")
          && !target.startsWith("data:")
        ) {
          element.removeAttribute(attribute);
        }
      }
    });
  }
  parsed.querySelectorAll("style").forEach((element) => {
    element.setAttribute("nonce", nonce);
  });
  for (const preserved of inlineScripts) {
    const script = parsed.createElement("script");
    script.setAttribute("nonce", nonce);
    if (preserved.type) script.setAttribute("type", preserved.type);
    script.textContent = preserved.source;
    parsed.body.appendChild(script);
  }
  const csp = parsed.createElement("meta");
  csp.setAttribute("http-equiv", "Content-Security-Policy");
  csp.setAttribute(
    "content",
    webviewContentSecurityPolicy(nonce, allowScripts, allowNetwork),
  );
  parsed.head.prepend(csp);
  return `<!doctype html>${parsed.documentElement.outerHTML}`;
}

/**
 * F-6 (task-3.md): a tiny emitter for workspace document events
 * (onDidSaveTextDocument / onDidChangeTextDocument / onDidOpenTextDocument).
 *
 * Extensions register a listener and receive a Disposable to remove it.
 * The host calls emit() when the editor signals a document save/change/open.
 * Listeners are stored in a Set so removal is O(1); a disposed emitter
 * drops all listeners and ignores future emit() calls.
 */
class SimpleEmitter<T> {
  private listeners = new Set<(e: T) => void>();
  private disposed = false;

  /** VS Code Event signature: register a listener, get a Disposable. */
  readonly event: Event<T> = (listener: (e: T) => void): Disposable => {
    if (this.disposed) {
      return { dispose: () => undefined };
    }
    this.listeners.add(listener);
    return {
      dispose: () => {
        this.listeners.delete(listener);
      },
    };
  };

  /** Fire the event to all registered listeners. Errors are swallowed. */
  emit(e: T): void {
    if (this.disposed) return;
    for (const l of this.listeners) {
      try {
        l(e);
      } catch {
        // A listener throwing must not break the other listeners.
      }
    }
  }

  /** Drop all listeners and mark the emitter disposed. */
  dispose(): void {
    this.disposed = true;
    this.listeners.clear();
  }
}

/**
 * F-6 (task-3.md): generate a random hex id of the given length.
 * Uses crypto.getRandomValues when available; falls back to Math.random
 * (sufficient for non-cryptographic session/machine IDs).
 */
function randomHexId(length: number): string {
  const bytes = new Uint8Array(length / 2);
  if (typeof crypto !== "undefined" && crypto.getRandomValues) {
    crypto.getRandomValues(bytes);
  } else {
    for (let i = 0; i < bytes.length; i++) {
      bytes[i] = Math.floor(Math.random() * 256);
    }
  }
  return Array.from(bytes)
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");
}

// Keep lazy loading for the extension host, but share the module namespace
// across concurrent terminal starts. Separate dynamic-import instances can
// otherwise expose distinct service objects in test/runtime module loaders,
// making two approved terminals observe different backend call surfaces.
let terminalServicesPromise: Promise<typeof import("@/api/services")> | null = null;
function loadTerminalServices(): Promise<typeof import("@/api/services")> {
  terminalServicesPromise ??= import("@/api/services");
  return terminalServicesPromise;
}

/** Serialize one argv value for AgentService's POSIX shlex parser. */
function quoteExecArgument(value: string): string {
  if (/^[A-Za-z0-9_./:@%+=,-]+$/.test(value)) return value;
  return `'${value.replace(/'/g, `'"'"'`)}'`;
}

/** Create an opaque task execution ID that remains unique across HMR/windows. */
function createTaskExecutionId(): string {
  return `task:${randomHexId(32)}`;
}

/**
 * F-6 (task-3.md): return a stable per-machine ID, persisted in
 * localStorage so it survives reloads. If localStorage is unavailable
 * (jsdom without the key, private mode), a fresh id is generated each call
 * — acceptable since the id is only used for telemetry-style uniqueness.
 */
function getOrCreateMachineId(): string {
  const key = "koyori-ide.extHost.machineId";
  try {
    const existing = localStorage.getItem(key);
    if (existing) return existing;
    const id = `machine-${randomHexId(16)}`;
    localStorage.setItem(key, id);
    return id;
  } catch {
    // localStorage may be unavailable; return an ephemeral id.
    return `machine-${randomHexId(16)}`;
  }
}

/**
 * F-6 (task-3.md): generate a fresh per-session ID. Called once per
 * ExtensionHost construction; each browser tab/process gets a unique id.
 */
function generateSessionId(): string {
  return `session-${randomHexId(16)}`;
}

/**
 * F-6 (task-3.md): minimal glob → RegExp converter for findFiles.
 *
 * Supports the common VS Code glob subset:
 *   - `**` matches any number of path segments (including zero)
 *   - `*`  matches any character except `/`
 *   - `?`  matches a single character except `/`
 *   - `.` and other regex metacharacters are escaped
 *
 * Anchored to the whole string (matches from start to end).
 */
function globToRegExp(glob: string): RegExp {
  let re = "^";
  for (let i = 0; i < glob.length; i++) {
    const c = glob[i];
    if (c === "*") {
      if (glob[i + 1] === "*") {
        // `**` — match anything (including `/`).
        re += ".*";
        i++; // skip the second `*`
        // Skip an optional following `/` so `**/` matches zero dirs too.
        if (glob[i + 1] === "/") {
          i++;
        }
      } else {
        // `*` — match anything except `/`.
        re += "[^/]*";
      }
    } else if (c === "?") {
      re += "[^/]";
    } else if ("\\^$.+|()[]{}".includes(c)) {
      re += "\\" + c;
    } else {
      re += c;
    }
  }
  re += "$";
  return new RegExp(re);
}

/**
 * F-6 (task-3.md): common file-extension → languageId map for
 * openTextDocument. Best-effort; covers the languages Monaco ships.
 */
const EXTENSION_TO_LANGUAGE: Record<string, string> = {
  ts: "typescript",
  tsx: "typescriptreact",
  js: "javascript",
  jsx: "javascriptreact",
  json: "json",
  md: "markdown",
  go: "go",
  rs: "rust",
  py: "python",
  rb: "ruby",
  java: "java",
  c: "c",
  h: "c",
  cpp: "cpp",
  cc: "cpp",
  hpp: "cpp",
  cs: "csharp",
  php: "php",
  swift: "swift",
  kt: "kotlin",
  sh: "shell",
  bash: "shell",
  yml: "yaml",
  yaml: "yaml",
  xml: "xml",
  html: "html",
  css: "css",
  scss: "scss",
  less: "less",
  vue: "vue",
  sql: "sql",
  toml: "toml",
  ini: "ini",
  txt: "plaintext",
};

// ---------------------------------------------------------------------------
// ExtensionHost
// ---------------------------------------------------------------------------

interface ActiveTaskExecution {
  task: Task;
  stopAccepted: boolean;
  stopPending: Promise<boolean> | null;
  completed: boolean;
}

interface ExtensionHostState {
  extensions: Map<string, ExtensionEntry>;
  commands: Map<string, RegisteredCommand>;
  approved: Set<string>;
  taskProviders: Map<string, { extensionId: string; provider: TaskProvider }>;
  taskExecutions: Map<string, ActiveTaskExecution>;
  debugConfigProviders: Map<
    string,
    { extensionId: string; provider: DebugConfigurationProvider }
  >;
  treeViewProviders: Map<
    string,
    { extensionId: string; provider: TreeDataProvider<unknown> }
  >;
  webviewViewProviders: Map<
    string,
    { extensionId: string; provider: WebviewViewProvider }
  >;
  clipboardBuffers: Map<string, string>;
  onDidSaveTextDocumentEmitter: SimpleEmitter<TextDocument>;
  onDidChangeTextDocumentEmitter: SimpleEmitter<TextDocumentChangeEvent>;
  onDidOpenTextDocumentEmitter: SimpleEmitter<TextDocument>;
}

function createExtensionHostState(
  approved = new Set<string>(),
): ExtensionHostState {
  return {
    extensions: new Map(),
    commands: new Map(),
    approved,
    taskProviders: new Map(),
    taskExecutions: new Map(),
    debugConfigProviders: new Map(),
    treeViewProviders: new Map(),
    webviewViewProviders: new Map(),
    clipboardBuffers: new Map(),
    onDidSaveTextDocumentEmitter: new SimpleEmitter<TextDocument>(),
    onDidChangeTextDocumentEmitter: new SimpleEmitter<TextDocumentChangeEvent>(),
    onDidOpenTextDocumentEmitter: new SimpleEmitter<TextDocument>(),
  };
}

/**
 * Manages the lifecycle and API surface for VS Code extensions. Each
 * extension receives an isolated `vscode` API object whose methods bridge
 * to koyori-ide services. All disposables registered by an extension are
 * tracked and disposed on deactivation.
 */
export class ExtensionHost {
  private state = createExtensionHostState();
  private options: ExtensionHostOptions;
  private get extensions() { return this.state.extensions; }
  private get commands() { return this.state.commands; }
  private get approved() { return this.state.approved; }
  private get taskProviders() { return this.state.taskProviders; }
  private get taskExecutions() { return this.state.taskExecutions; }
  private get debugConfigProviders() { return this.state.debugConfigProviders; }
  private get treeViewProviders() { return this.state.treeViewProviders; }
  private get webviewViewProviders() { return this.state.webviewViewProviders; }
  private get clipboardBuffers() { return this.state.clipboardBuffers; }
  private get onDidSaveTextDocumentEmitter() { return this.state.onDidSaveTextDocumentEmitter; }
  private get onDidChangeTextDocumentEmitter() { return this.state.onDidChangeTextDocumentEmitter; }
  private get onDidOpenTextDocumentEmitter() { return this.state.onDidOpenTextDocumentEmitter; }
  // F-6 (task-3.md): env API — stable per-machine and per-session IDs.
  // machineId is persisted in localStorage so it survives reloads; sessionId
  // is regenerated each time the host is constructed (per process/tab).
  private readonly _machineId: string;
  private readonly _sessionId: string;

  constructor(options: ExtensionHostOptions = {}) {
    this.options = options;
    extensionHostModuleState.hosts.add(new WeakRef(this));
    // F-6 (task-3.md): generate/persist machineId + sessionId.
    this._machineId = getOrCreateMachineId();
    this._sessionId = generateSessionId();
  }

  /** F-6 (task-3.md): stable per-machine ID (persisted in localStorage). */
  get machineId(): string {
    return this._machineId;
  }

  /** F-6 (task-3.md): stable per-session ID (regenerated per host instance). */
  get sessionId(): string {
    return this._sessionId;
  }

  // -------------------------------------------------------------------------
  // Activation / deactivation
  // -------------------------------------------------------------------------

  /**
   * Activate an extension by loading its main module via the configured
   * loader. Throws if no loader is configured or the module has no
   * `activate()` export.
   */
  async activate(desc: ExtensionDescriptor): Promise<void> {
    requireDeclaredPermissions(desc.permissions, desc.mainPath.trim().length > 0);
    if (!this.options.loadModule) {
      throw new Error(
        `ExtensionHost.activate: no loadModule configured; cannot load extension "${desc.id}" main at ${desc.mainPath}`,
      );
    }
    const module = await this.options.loadModule(desc.id, desc.mainPath);
    if (!module || typeof module.activate !== "function") {
      throw new Error(
        `Extension "${desc.id}" main module failed to load: no activate() export found at ${desc.mainPath}`,
      );
    }
    await this.activateWithModule(desc, module);
  }

  /**
   * Activate an extension with a pre-loaded module. This is the test entry
   * point (mirrors pluginRegistry's activatePluginWithModule) and the path
   * used when the host has already resolved the module.
   *
   * Steps:
   *   1. No-op if already active.
   *   2. Classify security level; refuse Restricted without approval.
   *   3. Register permissions so runtime gates can query them.
   *   4. Build the vscode API shim and call module.activate(api).
   *   5. On failure, roll back (dispose partials, unregister perms) and rethrow.
   */
  async activateWithModule(
    desc: ExtensionDescriptor,
    module: ExtensionModule,
  ): Promise<void> {
    if (this.isActive(desc.id)) return;

    const permissions = requireDeclaredPermissions(
      desc.permissions,
      desc.mainPath.trim().length > 0,
    );
    const normalizedDesc: ExtensionDescriptor & { permissions: ExtensionPermission[] } = {
      ...desc,
      permissions,
    };
    const level = classifyExtension(permissions);
    if (level === "Restricted" && !this.approved.has(desc.id)) {
      throw new Error(
        `Extension "${desc.id}" is Restricted and disabled by default; explicit user approval required (G-SEC-12)`,
      );
    }

    const entry: ExtensionEntry = {
      descriptor: normalizedDesc,
      module,
      disposables: [],
      securityLevel: level,
    };
    this.extensions.set(desc.id, entry);
    registerExtensionPermissions(desc.id, permissions);

    const bridge = this.createBridge(normalizedDesc);
    const api = createVscodeAPI(bridge);
    try {
      await module.activate(api);
    } catch (e) {
      // Roll back: dispose anything the extension registered before failing,
      // drop its permissions, and remove the tentative entry.
      this.disposeEntryDisposables(entry);
      unregisterExtensionPermissions(desc.id);
      this.extensions.delete(desc.id);
      throw e;
    }
  }

  /**
   * Deactivate an extension: call its `deactivate()` export (if any), then
   * dispose every tracked disposable, unregister its permissions, and drop
   * the entry. Idempotent — a no-op for extensions that are not active.
   */
  async deactivate(extensionId: string): Promise<void> {
    const entry = this.extensions.get(extensionId);
    if (!entry) return;

    // Call the extension's own deactivate() first so it can release
    // resources while its registered providers/commands still exist.
    let deactivationFailed = false;
    let deactivationError: unknown;
    try {
      if (typeof entry.module.deactivate === "function") {
        await entry.module.deactivate();
      }
    } catch (error) {
      deactivationFailed = true;
      deactivationError = error;
    }

    this.disposeEntryDisposables(entry);
    unregisterExtensionPermissions(extensionId);
    this.extensions.delete(extensionId);
    if (deactivationFailed) throw deactivationError;
  }

  /** Deactivate every active extension. Used on shutdown / project switch. */
  async disposeAll(): Promise<void> {
    const ids = Array.from(this.extensions.keys());
    let deactivationFailed = false;
    let firstError: unknown;
    try {
      for (const id of ids) {
        try {
          await this.deactivate(id);
        } catch (error) {
          if (!deactivationFailed) firstError = error;
          deactivationFailed = true;
        }
      }
    } finally {
      const approved = this.approved;
      this.onDidSaveTextDocumentEmitter.dispose();
      this.onDidChangeTextDocumentEmitter.dispose();
      this.onDidOpenTextDocumentEmitter.dispose();
      this.state = createExtensionHostState(approved);
    }
    if (deactivationFailed) throw firstError;
  }

  // -------------------------------------------------------------------------
  // State queries
  // -------------------------------------------------------------------------

  /** Whether an extension is currently active. */
  isActive(extensionId: string): boolean {
    return this.extensions.has(extensionId);
  }

  /** The classified security level, or undefined for inactive extensions. */
  getSecurityLevel(extensionId: string): SecurityLevel | undefined {
    return this.extensions.get(extensionId)?.securityLevel;
  }

  /**
   * Explicitly approve a Restricted extension so it may activate. Trusted
   * and Reviewed extensions do not need approval to activate (their
   * privileged operations are still permission-gated at runtime).
   */
  approveExtension(extensionId: string): void {
    this.approved.add(extensionId);
  }

  // -------------------------------------------------------------------------
  // Disposable tracking
  // -------------------------------------------------------------------------

  /**
   * Track a disposable for an extension so it is disposed when the
   * extension deactivates. Returns a disposable (the same handle) the
   * extension can dispose early if it wishes.
   */
  trackDisposable(extensionId: string, disposable: Disposable): Disposable {
    const entry = this.extensions.get(extensionId);
    if (!entry) {
      // Extension is not active (e.g. registering after deactivation).
      // Dispose immediately to avoid a leak.
      try {
        disposable.dispose();
      } catch {
        // ignore
      }
      return disposable;
    }
    entry.disposables.push(disposable);
    return disposable;
  }

  private untrackDisposable(
    extensionId: string,
    disposable: Disposable,
  ): void {
    const entry = this.extensions.get(extensionId);
    if (!entry) return;
    const index = entry.disposables.indexOf(disposable);
    if (index >= 0) entry.disposables.splice(index, 1);
  }

  /** Dispose all tracked disposables for an entry in reverse registration order. */
  private disposeEntryDisposables(entry: ExtensionEntry): void {
    for (let i = entry.disposables.length - 1; i >= 0; i--) {
      try {
        entry.disposables[i].dispose();
      } catch {
        // One disposable throwing must not skip the rest.
      }
    }
    entry.disposables.length = 0;
  }

  // -------------------------------------------------------------------------
  // Command registry (host-level)
  // -------------------------------------------------------------------------

  /**
   * Require explicit approval for a privileged runtime operation.
   *
   * Callers may retain and await the returned promise multiple times; the
   * handler is invoked exactly once for that promise. Only the operation id is
   * exposed to the handler so commands, paths, URLs, environment variables,
   * and terminal input never cross the confirmation boundary.
   */
  private async confirmRuntimeOperation(operation: string): Promise<void> {
    let approved = false;
    try {
      approved = this.options.confirmHandler
        ? await this.options.confirmHandler(operation, [])
        : false;
    } catch {
      // A missing or failing confirmation surface must fail closed.
    }
    if (!approved) {
      throw new Error(`Runtime operation "${operation}" denied`);
    }
  }

  /**
   * Execute a registered command. Dangerous commands (G-SEC-12) require
   * confirmation via the configured confirmHandler; default-deny when no
   * handler is set. Throws if the command is not registered.
   */
  async executeCommand(command: string, ...args: unknown[]): Promise<unknown> {
    // Dangerous commands are gated BEFORE the registry lookup (G-SEC-12):
    // a malicious extension must not learn whether a dangerous command is
    // registered, and the default-deny must fire even for unregistered ids.
    if (isDangerousCommand(command)) {
      await this.confirmRuntimeOperation(command);
    }
    const cmd = this.commands.get(command);
    if (!cmd) {
      throw new Error(`Command "${command}" is not registered`);
    }
    return cmd.handler(...args);
  }

  /** Register a command handler on behalf of an extension. */
  private registerCommandImpl(
    extensionId: string,
    command: string,
    callback: (...args: unknown[]) => unknown | Promise<unknown>,
  ): Disposable {
    const existing = this.commands.get(command);
    if (existing && existing.extensionId !== extensionId) {
      throw new Error(
        `Command "${command}" is already registered by extension "${existing.extensionId}"`,
      );
    }
    this.commands.set(command, { extensionId, handler: callback });
    const disposable: Disposable = {
      dispose: () => {
        // Only remove if still owned by this extension (avoids removing a
        // re-registered command owned by another extension).
        const cur = this.commands.get(command);
        if (cur && cur.extensionId === extensionId) {
          this.commands.delete(command);
        }
      },
    };
    this.trackDisposable(extensionId, disposable);
    return disposable;
  }

  // -------------------------------------------------------------------------
  // Monaco language-provider bridging
  // -------------------------------------------------------------------------

  /**
   * Bridge a vscode language provider to Monaco. The vscode DocumentSelector
   * is converted to a Monaco language id (the `language` field). The Monaco
   * disposable is tracked for cleanup on deactivation.
   *
   * F-6 (task-3.md): extended to cover all 21 VS Code languages API provider
   * kinds. When Monaco lacks the optional registration method for a kind,
   * the host returns a no-op disposable (still tracked for cleanup) so the
   * extension can call dispose() without error.
   */
  private bridgeLanguageProviderImpl(
    extensionId: string,
    kind: LanguageProviderKind,
    selector: DocumentSelector,
    provider: unknown,
    extra?: unknown,
  ): Disposable {
    const monaco = this.options.monaco;
    const language = selector.language;
    if (!monaco) {
      // No Monaco available (e.g. test without monaco option). Return a
      // no-op disposable so the extension can still call dispose().
      const noop: Disposable = { dispose: () => undefined };
      this.trackDisposable(extensionId, noop);
      return noop;
    }
    const noop: Disposable = { dispose: () => undefined };
    let monacoDisposable: Disposable;
    switch (kind) {
      case "completion":
        monacoDisposable = monaco.languages.registerCompletionItemProvider(
          language,
          provider,
        );
        break;
      case "hover":
        monacoDisposable = monaco.languages.registerHoverProvider(
          language,
          provider,
        );
        break;
      case "definition":
        monacoDisposable = monaco.languages.registerDefinitionProvider
          ? monaco.languages.registerDefinitionProvider(language, provider)
          : noop;
        break;
      case "codeAction":
        monacoDisposable = monaco.languages.registerCodeActionProvider
          ? monaco.languages.registerCodeActionProvider(language, provider)
          : noop;
        break;
      // F-6: 17 additional kinds — each is optional on MonacoBridge.
      case "reference":
        monacoDisposable = monaco.languages.registerReferenceProvider
          ? monaco.languages.registerReferenceProvider(language, provider)
          : noop;
        break;
      case "codeLens":
        monacoDisposable = monaco.languages.registerCodeLensProvider
          ? monaco.languages.registerCodeLensProvider(language, provider)
          : noop;
        break;
      case "documentFormatting":
        monacoDisposable = monaco.languages.registerDocumentFormattingEditProvider
          ? monaco.languages.registerDocumentFormattingEditProvider(language, provider)
          : noop;
        break;
      case "documentRangeFormatting":
        monacoDisposable = monaco.languages.registerDocumentRangeFormattingEditProvider
          ? monaco.languages.registerDocumentRangeFormattingEditProvider(language, provider)
          : noop;
        break;
      case "onTypeFormatting": {
        const e = (extra ?? {}) as {
          firstTriggerCharacter?: string;
          moreTriggerCharacter?: string[];
        };
        monacoDisposable = monaco.languages.registerOnTypeFormattingEditProvider
          ? monaco.languages.registerOnTypeFormattingEditProvider(
              language,
              provider,
              e.firstTriggerCharacter ?? "",
              e.moreTriggerCharacter,
            )
          : noop;
        break;
      }
      case "signatureHelp":
        monacoDisposable = monaco.languages.registerSignatureHelpProvider
          ? monaco.languages.registerSignatureHelpProvider(language, provider)
          : noop;
        break;
      case "workspaceSymbol":
        monacoDisposable = monaco.languages.registerWorkspaceSymbolProvider
          ? monaco.languages.registerWorkspaceSymbolProvider(provider)
          : noop;
        break;
      case "documentLink":
        monacoDisposable = monaco.languages.registerDocumentLinkProvider
          ? monaco.languages.registerDocumentLinkProvider(language, provider)
          : noop;
        break;
      case "color":
        monacoDisposable = monaco.languages.registerColorProvider
          ? monaco.languages.registerColorProvider(language, provider)
          : noop;
        break;
      case "foldingRange":
        monacoDisposable = monaco.languages.registerFoldingRangeProvider
          ? monaco.languages.registerFoldingRangeProvider(language, provider)
          : noop;
        break;
      case "declaration":
        monacoDisposable = monaco.languages.registerDeclarationProvider
          ? monaco.languages.registerDeclarationProvider(language, provider)
          : noop;
        break;
      case "implementation":
        monacoDisposable = monaco.languages.registerImplementationProvider
          ? monaco.languages.registerImplementationProvider(language, provider)
          : noop;
        break;
      case "typeDefinition":
        monacoDisposable = monaco.languages.registerTypeDefinitionProvider
          ? monaco.languages.registerTypeDefinitionProvider(language, provider)
          : noop;
        break;
      case "rename":
        monacoDisposable = monaco.languages.registerRenameProvider
          ? monaco.languages.registerRenameProvider(language, provider)
          : noop;
        break;
      case "documentSymbol":
        monacoDisposable = monaco.languages.registerDocumentSymbolProvider
          ? monaco.languages.registerDocumentSymbolProvider(language, provider)
          : noop;
        break;
      case "documentSemanticTokens":
        monacoDisposable = monaco.languages.registerDocumentSemanticTokensProvider
          ? monaco.languages.registerDocumentSemanticTokensProvider(language, provider)
          : noop;
        break;
      case "documentHighlight":
        monacoDisposable = monaco.languages.registerDocumentHighlightProvider
          ? monaco.languages.registerDocumentHighlightProvider(language, provider)
          : noop;
        break;
      case "inlayHints":
        monacoDisposable = monaco.languages.registerInlayHintsProvider
          ? monaco.languages.registerInlayHintsProvider(language, provider)
          : noop;
        break;
      default: {
        // Exhaustiveness guard — if a new kind is added to
        // LanguageProviderKind without a case here, TypeScript will
        // complain that `kind` is not assignable to `never`.
        const _exhaustive: never = kind;
        void _exhaustive;
        monacoDisposable = noop;
      }
    }
    this.trackDisposable(extensionId, monacoDisposable);
    return monacoDisposable;
  }

  // -------------------------------------------------------------------------
  // workspace.fs bridging (permission-gated, backend pathsec-validated)
  // -------------------------------------------------------------------------

  /** Resolve and normalize a URI path, then enforce a workspace boundary. */
  private resolveWorkspacePath(fsPath: string, root: string): string {
    const decodedRoot = decodeWorkspacePath(root);
    const normalizedRoot = normalizeWorkspacePath(decodedRoot).replace(
      /\/$/,
      "",
    );
    if (!normalizedRoot || !isAbsoluteWorkspacePath(normalizedRoot)) {
      throw new Error(
        `Path traversal denied: workspace root "${root}" is not absolute`,
      );
    }
    const decodedPath = decodeWorkspacePath(fsPath);
    if (/^[A-Za-z]:/.test(decodedPath) && !/^[A-Za-z]:\//.test(decodedPath)) {
      throw new Error(
        `Path traversal denied: drive-relative path "${fsPath}" is not allowed`,
      );
    }
    const candidate = normalizeWorkspacePath(
      isAbsoluteWorkspacePath(decodedPath)
        ? decodedPath
        : `${normalizedRoot}/${decodedPath}`,
    );
    const windowsPath = /^[A-Za-z]:\//.test(normalizedRoot);
    const comparableRoot = windowsPath
      ? normalizedRoot.toLowerCase()
      : normalizedRoot;
    const comparableCandidate = windowsPath ? candidate.toLowerCase() : candidate;
    if (
      comparableCandidate !== comparableRoot &&
      !comparableCandidate.startsWith(`${comparableRoot}/`)
    ) {
      throw new Error(
        `Path traversal denied: "${candidate}" is outside workspace root "${root}"`,
      );
    }
    return candidate;
  }

  /** Bridge vscode.workspace.fs.readFile → FileService.readFile. */
  private async bridgeReadFileImpl(
    extensionId: string,
    uri: Uri,
  ): Promise<Uint8Array> {
    if (!hasPermission(extensionId, "fs.read")) {
      throw new Error(
        `Extension "${extensionId}" cannot read files: requires permission "fs.read" not declared`,
      );
    }
    const { fileService } = await import("@/api/services");
    const { appState } = await import("@/stores/app");
    const fullPath = this.resolveWorkspacePath(
      uri.fsPath,
      appState.currentProject ?? "",
    );
    const content = await fileService.readFile(fullPath);
    // Re-construct via the current realm's Uint8Array so `instanceof Uint8Array`
    // holds across jsdom/Node realm boundaries (TextEncoder may produce a
    // Uint8Array backed by a different realm's constructor).
    const encoded = new TextEncoder().encode(content);
    return new Uint8Array(encoded);
  }

  /** Bridge vscode.workspace.fs.writeFile → FileService.writeFile. */
  private async bridgeWriteFileImpl(
    extensionId: string,
    uri: Uri,
    content: Uint8Array,
  ): Promise<void> {
    if (!hasPermission(extensionId, "fs.write")) {
      throw new Error(
        `Extension "${extensionId}" cannot write files: requires permission "fs.write" not declared`,
      );
    }
    const { fileService } = await import("@/api/services");
    const { appState } = await import("@/stores/app");
    const fullPath = this.resolveWorkspacePath(
      uri.fsPath,
      appState.currentProject ?? "",
    );
    const text = new TextDecoder().decode(content);
    await fileService.writeFile(fullPath, text);
  }

  /** Bridge vscode.workspace.fs.exists → FileService (via readFile probe). */
  private async bridgeExistsImpl(
    extensionId: string,
    uri: Uri,
  ): Promise<boolean> {
    if (!hasPermission(extensionId, "fs.read")) {
      throw new Error(
        `Extension "${extensionId}" cannot stat files: requires permission "fs.read" not declared`,
      );
    }
    try {
      await this.bridgeReadFileImpl(extensionId, uri);
      return true;
    } catch {
      return false;
    }
  }

  /** Bridge vscode.workspace.fs.createDirectory → FileService.createDirectory. */
  private async bridgeCreateDirectoryImpl(
    extensionId: string,
    uri: Uri,
  ): Promise<void> {
    if (!hasPermission(extensionId, "fs.write")) {
      throw new Error(
        `Extension "${extensionId}" cannot create directories: requires permission "fs.write" not declared`,
      );
    }
    const { fileService } = await import("@/api/services");
    const { appState } = await import("@/stores/app");
    const fullPath = this.resolveWorkspacePath(
      uri.fsPath,
      appState.currentProject ?? "",
    );
    await fileService.createDirectory(fullPath);
  }

  // -------------------------------------------------------------------------
  // Webview panel bridging (G-SEC-05 sandboxed iframe)
  // -------------------------------------------------------------------------

  /**
   * Create a webview panel backed by a sandboxed iframe. The iframe uses
   * `sandbox="allow-scripts"` (no allow-same-origin) so the extension's
   * HTML cannot reach the parent DOM, localStorage, or Wails bindings —
   * only the postMessage RPC bridge (mirrors PluginViewIframe.vue).
   */
  private createWebviewPanelImpl(
    extensionId: string,
    viewType: string,
    title: string,
    _showOptions: unknown,
    options: unknown,
  ): WebviewPanel {
    if (!hasPermission(extensionId, "ui.webview")) {
      throw new Error(
        `Extension "${extensionId}" cannot create webview panels: requires permission "ui.webview" not declared`,
      );
    }
    const allowNetwork = hasPermission(extensionId, "network");
    const enableScripts = webviewScriptsEnabled(options);
    if (enableScripts && !allowNetwork) {
      throw new Error(
        `Extension "${extensionId}" cannot enable webview scripts without permission "network"`,
      );
    }

    const iframe = document.createElement("iframe");
    iframe.setAttribute("sandbox", enableScripts ? "allow-scripts" : "");
    // The host mounts the iframe visibly when it decides where to show the
    // panel; until then it lives in the DOM (hidden) so tests and the host
    // can interact with it.
    iframe.style.display = "none";
    iframe.title = title;
    document.body.appendChild(iframe);

    let html = "";
    let disposed = false;
    const disposeListeners: Array<() => void> = [];
    const removeIframe = () => {
      try {
        iframe.srcdoc = "";
        iframe.removeAttribute("src");
      } catch {
        // Continue with DOM removal even if content reset fails.
      }
      try {
        iframe.remove();
      } catch {
        // Fall back to removeChild below for older or patched DOMs.
      }
      if (iframe.parentNode) {
        try {
          iframe.parentNode.removeChild(iframe);
        } catch {
          // A later idempotent dispose call can retry the removal.
        }
      }
      if (!iframe.parentNode) {
        extensionHostModuleState.webviewIframeDisposers.delete(iframe);
      }
    };
    extensionHostModuleState.webviewIframeDisposers.set(iframe, removeIframe);

    const webview: Webview = {
      get html() {
        return html;
      },
      set html(value: string) {
        if (disposed) return;
        html = value;
        iframe.srcdoc = createSecureWebviewSrcdoc(value, enableScripts, allowNetwork);
      },
      get _iframe() {
        return iframe;
      },
    };

    const panel: WebviewPanel = {
      viewType,
      title,
      webview,
      visible: true,
      active: true,
      dispose() {
        const firstDispose = !disposed;
        disposed = true;
        html = "";
        try {
          (extensionHostModuleState.webviewIframeDisposers.get(iframe) ?? removeIframe)();
        } catch {
          // Listener cleanup below must still run if DOM cleanup fails.
        }
        if (!firstDispose) return;
        for (const l of disposeListeners) {
          try {
            l();
          } catch {
            // ignore listener errors
          }
        }
        disposeListeners.length = 0;
      },
      onDidDispose(listener: () => void): Disposable {
        if (disposed) return { dispose() {} };
        disposeListeners.push(listener);
        return {
          dispose: () => {
            const i = disposeListeners.indexOf(listener);
            if (i >= 0) disposeListeners.splice(i, 1);
          },
        };
      },
    };

    // Track the panel so it is disposed (iframe removed) on deactivation.
    this.trackDisposable(extensionId, { dispose: () => panel.dispose() });
    return panel;
  }

  // -------------------------------------------------------------------------
  // Notifications
  // -------------------------------------------------------------------------

  /**
   * Surface an extension notification. v1 best-effort: log to the console.
   * A richer surface (lib/notifications) is gated behind the
   * `ui.notifications` permission in a future iteration; for now we log so
   * the API is usable without pulling the notification module's deps.
   */
  private notifyImpl(
    extensionId: string,
    level: "info" | "warn" | "error",
    message: string,
  ): void {
    if (this.options.onNotify) {
      this.options.onNotify(level, `[ext:${extensionId}] ${message}`);
      return;
    }
    // No UI surface wired: degrade to console (Partial), never fake success.
    const fn =
      level === "error" ? console.error : level === "warn" ? console.warn : console.log;
    fn(`[ext:${extensionId}] ${message}`);
  }

  private notifyExtensionImpl(
    extensionId: string,
    level: "info" | "warn" | "error",
    message: string,
  ): void {
    if (!hasPermission(extensionId, "ui.notifications")) {
      throw new Error(
        `Extension "${extensionId}" cannot show notifications: requires permission "ui.notifications" not declared`,
      );
    }
    this.notifyImpl(extensionId, level, message);
  }

  // -------------------------------------------------------------------------
  // F-6 (task-3.md): tasks API bridging
  //
  // - registerTaskProvider: stores the provider in a host-level registry
  //   keyed by task type. Returns a disposable that removes the provider.
  // - fetchTasks: collects tasks from all registered providers (awaiting
  //   provideTasks()) plus the backend TaskService.LoadTasks (converted
  //   from TaskDef → Task).
  // - executeTask: permission-gated by `tasks.execute`. Runs the task's
  //   ShellExecution via the backend AgentService.ExecCommand (which
  //   already enforces cwd + captures stdout/stderr). Returns a
  //   TaskExecution handle whose terminate() forwards the execution id to
  //   the backend TaskService stop hook on a best-effort basis.
  // -------------------------------------------------------------------------

  /** Register a task provider for a task type. Returns a disposable. */
  private bridgeRegisterTaskProviderImpl(
    extensionId: string,
    type: string,
    provider: TaskProvider,
  ): Disposable {
    const existing = this.taskProviders.get(type);
    if (existing && existing.extensionId !== extensionId) {
      throw new Error(
        `Task provider type "${type}" is already registered by extension "${existing.extensionId}"`,
      );
    }
    const registration = { extensionId, provider };
    // Same-owner replacement is allowed. Identity checks make the previous
    // disposable inert so it cannot remove the replacement.
    this.taskProviders.set(type, registration);
    const disposable: Disposable = {
      dispose: () => {
        if (this.taskProviders.get(type) === registration) {
          this.taskProviders.delete(type);
        }
      },
    };
    this.trackDisposable(extensionId, disposable);
    return disposable;
  }

  /**
   * Fetch all tasks: invoke each registered provider's provideTasks(),
   * then merge in backend TaskService.LoadTasks (TaskDef → Task).
   */
  private async bridgeFetchTasksImpl(extensionId: string): Promise<Task[]> {
    const out: Task[] = [];
    // 1. Tasks from registered providers.
    for (const [type, entry] of this.taskProviders) {
      try {
        const result = await entry.provider.provideTasks();
        if (result) {
          for (const task of result) {
            // Ensure definition.type reflects the registered type.
            out.push({
              ...task,
              definition: { ...task.definition, type: task.definition.type || type },
            });
          }
        }
      } catch {
        // A failing provider must not break fetchTasks for the rest.
      }
    }
    // 2. Tasks from the backend TaskService (TaskDef list).
    try {
      const { taskService } = await import("@/api/services");
      const { appState } = await import("@/stores/app");
      const root = appState.currentProject ?? "";
      if (root) {
        const defs = await taskService.loadTasks(root);
        for (const def of defs) {
          out.push({
            name: def.label,
            source: "workspace",
            definition: { type: "shell" },
            execution: {
              command: def.command,
              args: def.args,
              cwd: def.cwd,
              shell: def.shell ?? true,
            },
          });
        }
      }
    } catch {
      // Backend may be unavailable (tests); skip silently.
    }
    // Suppress unused-parameter warning while keeping the signature stable.
    void extensionId;
    return out;
  }

  /**
   * Execute a task. Requires the `tasks.execute` permission. Runs the
   * ShellExecution via TaskService.Execute, which tracks a cancellable
   * backend process under the same opaque ID used by TaskExecution.terminate().
   */
  private async bridgeExecuteTaskImpl(
    extensionId: string,
    task: Task,
  ): Promise<TaskExecution> {
    if (!hasPermission(extensionId, "tasks.execute")) {
      throw new Error(
        `Extension "${extensionId}" cannot execute tasks: requires permission "tasks.execute" not declared`,
      );
    }
    await this.confirmRuntimeOperation("tasks.executeTask");
    this.assertActivePermission(extensionId, "tasks.execute", "execute tasks");
    const { appState } = await import("@/stores/app");
    this.assertActivePermission(extensionId, "tasks.execute", "execute tasks");
    const cwd = task.execution.cwd ?? appState.currentProject ?? "";
    const fullCommand =
      task.execution.args && task.execution.args.length > 0
        ? `${task.execution.command} ${task.execution.args.map(quoteExecArgument).join(" ")}`
        : task.execution.command;
    const id = createTaskExecutionId();
    let resolveCompletion!: () => void;
    const completion = new Promise<void>((resolve) => {
      resolveCompletion = resolve;
    });
    const record = {
      task,
      stopAccepted: false,
      stopPending: null as Promise<boolean> | null,
      completed: false,
    };
    this.taskExecutions.set(id, record);
    const requestStop = (): Promise<boolean> => {
      if (record.completed || record.stopAccepted) return Promise.resolve(true);
      if (record.stopPending) return record.stopPending;
      const stopRequest = (async (): Promise<boolean> => {
        try {
          await TaskServiceBindings.Stop(id);
          record.stopAccepted = true;
          return true;
        } catch (error: unknown) {
          const message = error instanceof Error ? error.message : String(error);
          this.notifyImpl(
            extensionId,
            "error",
            `Failed to terminate task "${task.name}": ${message}`,
          );
          return false;
        } finally {
          record.stopPending = null;
        }
      })();
      record.stopPending = stopRequest;
      return stopRequest;
    };
    // The generated binding starts the backend request before the handle is
    // returned, while the unresolved result remains fire-and-forget like VS Code tasks.
    let execPromise: Promise<boolean>;
    try {
      const approvalToken = await TaskServiceBindings.RequestExecutionApproval(
        id,
        fullCommand,
        cwd,
      );
      execPromise = TaskServiceBindings.ExecuteApproved(
        id,
        fullCommand,
        cwd,
		approvalToken,
      ).then(
        () => true,
        async (error: unknown) => {
          const message = error instanceof Error ? error.message : String(error);
          this.notifyImpl(extensionId, "error", `Task "${task.name}" failed: ${message}`);
          // Execute can reject while retaining a retryable backend handle
          // (for example, when a process-tree termination attempt failed).
          return requestStop();
        },
      );
    } catch (error) {
      this.taskExecutions.delete(id);
      throw error;
    }
    const execution: TaskExecution = {
      task,
      completion,
      terminate: () => {
        void requestStop();
      },
    };
    const executionDisposable: Disposable = { dispose: execution.terminate };
    this.trackDisposable(extensionId, executionDisposable);
    // When the command finishes, mark the handle inactive and release it.
    void execPromise.then((canRelease) => {
      if (!canRelease) return;
      record.completed = true;
      this.taskExecutions.delete(id);
      this.untrackDisposable(extensionId, executionDisposable);
      resolveCompletion();
    });
    return execution;
  }

  // -------------------------------------------------------------------------
  // F-6 (task-3.md): debug API bridging
  //
  // - registerDebugConfigurationProvider: stores the provider in a host-
  //   level registry keyed by debug type. Returns a disposable.
  // - startDebugging: permission-gated by `debug.execute`. Resolves the
  //   config via the matching provider's resolveDebugConfiguration()
  //   (if any), then calls DebugService.launchWithConfig. Returns true
  //   on success, false on failure.
  // -------------------------------------------------------------------------

  /** Register a debug configuration provider for a debug type. */
  private bridgeRegisterDebugConfigurationProviderImpl(
    extensionId: string,
    type: string,
    provider: DebugConfigurationProvider,
  ): Disposable {
    const existing = this.debugConfigProviders.get(type);
    if (existing && existing.extensionId !== extensionId) {
      throw new Error(
        `Debug configuration provider type "${type}" is already registered by extension "${existing.extensionId}"`,
      );
    }
    const registration = { extensionId, provider };
    this.debugConfigProviders.set(type, registration);
    const disposable: Disposable = {
      dispose: () => {
        if (this.debugConfigProviders.get(type) === registration) {
          this.debugConfigProviders.delete(type);
        }
      },
    };
    this.trackDisposable(extensionId, disposable);
    return disposable;
  }

  /**
   * Start a debug session. Requires the `debug.execute` permission.
   * Resolves the config via the registered provider (if any), then
   * calls backend DebugService.launchWithConfig.
   */
  private async bridgeStartDebuggingImpl(
    extensionId: string,
    folder: WorkspaceFolder | undefined,
    config: DebugConfiguration,
  ): Promise<boolean> {
    if (!hasPermission(extensionId, "debug.execute")) {
      throw new Error(
        `Extension "${extensionId}" cannot start debugging: requires permission "debug.execute" not declared`,
      );
    }
    await this.confirmRuntimeOperation("debug.startDebugging");
    this.assertActivePermission(extensionId, "debug.execute", "start debugging");
    // Let the matching provider resolve/modify the config before launch.
    let resolvedConfig = config;
    const entry = this.debugConfigProviders.get(config.type);
    if (entry && typeof entry.provider.resolveDebugConfiguration === "function") {
      try {
        const result = await entry.provider.resolveDebugConfiguration(folder, config);
        if (result === null) {
          // Provider signaled abort.
          return false;
        }
        if (result) {
          resolvedConfig = result;
        }
      } catch {
        // A failing resolver must not block launch; proceed with original config.
      }
    }
    try {
      this.assertActivePermission(extensionId, "debug.execute", "start debugging");
      const { debugService } = await import("@/api/services");
      const { appState } = await import("@/stores/app");
      this.assertActivePermission(extensionId, "debug.execute", "start debugging");
      // Map DebugConfiguration → DebugService.launchWithConfig shape.
      // The `kind` field is derived from `config.type` (e.g. "go", "node").
      // `dir` defaults to the workspace root or the folder uri.
      const dir = resolvedConfig.cwd ?? folder?.uri?.fsPath ?? appState.currentProject ?? "";
      await debugService.launchWithConfig({
        name: resolvedConfig.name,
        kind: resolvedConfig.type,
        dir,
        program: resolvedConfig.program,
        args: resolvedConfig.args,
        env: resolvedConfig.env,
        stopOnEntry: resolvedConfig.stopOnEntry,
        mode: resolvedConfig.mode,
      });
      return true;
    } catch (e: unknown) {
      const msg = e instanceof Error ? e.message : String(e);
      this.notifyImpl(extensionId, "error", `Debug launch failed: ${msg}`);
      return false;
    }
  }

  // -------------------------------------------------------------------------
  // F-6 (task-3.md): scm API bridging
  //
  // createSourceControl returns a SourceControl object backed by the
  // backend GitService. The SourceControl exposes:
  //   - inputBox: a { value, placeholder } object (the commit message box)
  //   - createResourceGroup(id, label): returns a group with a
  //     resourceStates array. The group's dispose() removes it.
  //   - dispose(): removes the source control and all its groups.
  //
  // Resource groups are populated by the extension (or by the host on
  // createSourceControl by reading GitService.GetStatus). The host does
  // NOT auto-refresh — extensions call gitService via the bridge if they
  // want live data.
  // -------------------------------------------------------------------------

  /**
   * Create a source control. Permission-gated by `scm.read` (creating a
   * source control implies reading git state). Returns a SourceControl
   * whose lifecycle is tracked for disposal.
   */
  private bridgeCreateSourceControlImpl(
    extensionId: string,
    id: string,
    label: string,
    rootUri: Uri | undefined,
  ): SourceControl {
    if (!hasPermission(extensionId, "scm.read")) {
      throw new Error(
        `Extension "${extensionId}" cannot create source control: requires permission "scm.read" not declared`,
      );
    }
    const groups = new Map<string, SourceControlResourceGroup>();
    let disposed = false;

    const inputBox: SourceControlInputBox = {
      value: "",
      placeholder: "Message (press Enter to commit)",
    };

    const sc: SourceControl = {
      id,
      label,
      rootUri,
      inputBox,
      createResourceGroup(groupId: string, groupLabel: string): SourceControlResourceGroup {
        if (disposed) {
          throw new Error(`SourceControl "${id}" is disposed`);
        }
        const group: SourceControlResourceGroup = {
          id: groupId,
          label: groupLabel,
          resourceStates: [] as SourceControlResourceState[],
          dispose() {
            // No-op if already removed.
            groups.delete(groupId);
          },
        };
        groups.set(groupId, group);
        return group;
      },
      dispose() {
        if (disposed) return;
        disposed = true;
        for (const g of groups.values()) {
          try {
            g.dispose();
          } catch {
            // ignore
          }
        }
        groups.clear();
      },
    };

    // Track the source control so it is disposed on extension deactivation.
    this.trackDisposable(extensionId, { dispose: () => sc.dispose() });
    return sc;
  }

  // -------------------------------------------------------------------------
  // F-6 (task-3.md): workspace API 补齐 bridging
  //
  // - fs.rename: permission-gated by fs.write. Bridges to FileService.renamePath.
  // - fs.delete: permission-gated by fs.write. Bridges to FileService.deletePath.
  // - fs.readDirectory: permission-gated by fs.read. Bridges to FileService.listDirectory.
  // - findFiles: permission-gated by fs.read. Bridges to FileService.listAllFiles
  //   then filters by the include glob (string or {base,pattern}).
  // - findTextInFiles: permission-gated by fs.read. Bridges to SearchService.Search.
  // - openTextDocument: permission-gated by fs.read. Reads the file and wraps
  //   it in a TextDocument (languageId inferred from extension).
  // - saveAll: best-effort v1 — returns true (no live editor wiring).
  // - onDidSaveTextDocument/onDidChangeTextDocument/onDidOpenTextDocument:
  //   expose the host's shared emitters as VS Code Events.
  // -------------------------------------------------------------------------

  /** Bridge vscode.workspace.fs.rename → FileService.renamePath. */
  private async bridgeRenameImpl(
    extensionId: string,
    oldUri: Uri,
    newUri: Uri,
    options?: { overwrite?: boolean },
  ): Promise<void> {
    if (!hasPermission(extensionId, "fs.write")) {
      throw new Error(
        `Extension "${extensionId}" cannot rename files: requires permission "fs.write" not declared`,
      );
    }
    const { fileService } = await import("@/api/services");
    const { appState } = await import("@/stores/app");
    const root = appState.currentProject ?? "";
    const oldPath = this.resolveWorkspacePath(oldUri.fsPath, root);
    const newPath = this.resolveWorkspacePath(newUri.fsPath, root);
    // If overwrite is false (default), probe the destination to avoid
    // clobbering. The backend renamePath overwrites unconditionally, so we
    // enforce the non-overwrite contract here.
    if (!options?.overwrite) {
      try {
        await fileService.readFile(newPath);
        throw new Error(
          `Rename target "${newPath}" already exists and overwrite was not requested`,
        );
      } catch (e) {
        // readFile failing means the target does not exist — proceed.
        const msg = e instanceof Error ? e.message : String(e);
        if (msg.includes("already exists")) throw e;
      }
    }
    await fileService.renamePath(oldPath, newPath);
  }

  /** Bridge vscode.workspace.fs.delete → FileService.deletePath. */
  private async bridgeDeleteImpl(
    extensionId: string,
    uri: Uri,
    _options?: { recursive?: boolean; useTrash?: boolean },
  ): Promise<void> {
    if (!hasPermission(extensionId, "fs.write")) {
      throw new Error(
        `Extension "${extensionId}" cannot delete files: requires permission "fs.write" not declared`,
      );
    }
    const { fileService } = await import("@/api/services");
    const { appState } = await import("@/stores/app");
    const fullPath = this.resolveWorkspacePath(
      uri.fsPath,
      appState.currentProject ?? "",
    );
    // FileService.deletePath deletes recursively already; useTrash is
    // ignored in v1 (no trash integration).
    await fileService.deletePath(fullPath);
  }

  /** Bridge vscode.workspace.fs.readDirectory → FileService.listDirectory. */
  private async bridgeReadDirectoryImpl(
    extensionId: string,
    uri: Uri,
  ): Promise<[string, FileType][]> {
    if (!hasPermission(extensionId, "fs.read")) {
      throw new Error(
        `Extension "${extensionId}" cannot read directories: requires permission "fs.read" not declared`,
      );
    }
    const { fileService } = await import("@/api/services");
    const { appState } = await import("@/stores/app");
    const fullPath = this.resolveWorkspacePath(
      uri.fsPath,
      appState.currentProject ?? "",
    );
    const entries = await fileService.listDirectory(fullPath);
    return entries.map((e) => [
      e.name,
      e.isDir ? FileType.Directory : FileType.File,
    ] as [string, FileType]);
  }

  /**
   * Bridge vscode.workspace.findFiles → FileService.listAllFiles filtered
   * by the include glob. Permission-gated by fs.read.
   *
   * Glob handling: v1 supports simple glob patterns via a minimal matcher
   * (`*` → `[^/]*`, `**` → `.*`, `?` → `[^/]`). The exclude pattern is
   * applied as a deny filter; maxResults caps the output length.
   */
  private async bridgeFindFilesImpl(
    extensionId: string,
    include: GlobPattern,
    exclude: GlobPattern | undefined,
    maxResults?: number,
  ): Promise<Uri[]> {
    if (!hasPermission(extensionId, "fs.read")) {
      throw new Error(
        `Extension "${extensionId}" cannot find files: requires permission "fs.read" not declared`,
      );
    }
    const { fileService } = await import("@/api/services");
    const { appState } = await import("@/stores/app");
    const root = appState.currentProject ?? "";
    if (!root) return [];
    const allFiles = await fileService.listAllFiles(root);
    const includeRe = globToRegExp(typeof include === "string" ? include : include.pattern);
    const excludeRe = exclude ? globToRegExp(typeof exclude === "string" ? exclude : exclude.pattern) : null;
    const out: Uri[] = [];
    for (const relPath of allFiles) {
      if (!includeRe.test(relPath)) continue;
      if (excludeRe && excludeRe.test(relPath)) continue;
      out.push({ fsPath: relPath, scheme: "file" });
      if (maxResults && out.length >= maxResults) break;
    }
    return out;
  }

  /**
   * Bridge vscode.workspace.findTextInFiles → SearchService.Search.
   * Permission-gated by fs.read.
   */
  private async bridgeFindTextInFilesImpl(
    extensionId: string,
    query: TextSearchQuery,
    options: FindTextInFilesOptions,
  ): Promise<TextSearchResult[]> {
    if (!hasPermission(extensionId, "fs.read")) {
      throw new Error(
        `Extension "${extensionId}" cannot search files: requires permission "fs.read" not declared`,
      );
    }
    const { searchService } = await import("@/api/services");
    const { appState } = await import("@/stores/app");
    const root = appState.currentProject ?? "";
    if (!root) return [];
    const ignoreCase = options.ignoreCase ?? (query.isCaseSensitive ? false : true);
    const results = await searchService.search(root, query.pattern, ignoreCase);
    // Convert backend SearchResult → vscode TextSearchResult.
    const out: TextSearchResult[] = [];
    let total = 0;
    for (const r of results) {
      const matches = r.matches.map((m) => ({
        uri: { fsPath: r.path, scheme: "file" } as Uri,
        line: m.line,
        column: m.column,
        preview: m.preview,
      }));
      out.push({ uri: { fsPath: r.path, scheme: "file" } as Uri, matches });
      total += matches.length;
      if (options.maxResults && total >= options.maxResults) break;
    }
    return out;
  }

  /**
   * Bridge vscode.workspace.openTextDocument → FileService.readFile.
   * Returns a TextDocument wrapping the file content. Permission-gated by
   * fs.read. The languageId is inferred from the file extension.
   */
  private async bridgeOpenTextDocumentImpl(
    extensionId: string,
    uri: Uri,
  ): Promise<TextDocument> {
    if (!hasPermission(extensionId, "fs.read")) {
      throw new Error(
        `Extension "${extensionId}" cannot open documents: requires permission "fs.read" not declared`,
      );
    }
    const content = await this.bridgeReadFileImpl(extensionId, uri);
    const text = new TextDecoder().decode(content);
    // Infer languageId from extension (best-effort).
    const ext = uri.fsPath.split(".").pop()?.toLowerCase() ?? "";
    const languageId = EXTENSION_TO_LANGUAGE[ext] ?? "plaintext";
    const doc: TextDocument = {
      uri,
      languageId,
      getText: () => text,
    };
    // Fire onDidOpenTextDocument for any registered listeners.
    this.onDidOpenTextDocumentEmitter.emit(doc);
    return doc;
  }

  /**
   * Bridge vscode.workspace.saveAll. G13: real save through the injected
   * editor bridge — every dirty buffer is flushed and per-file failures are
   * propagated (fail-closed). No bridge means no fake success: the call
   * throws a versioned unsupported error instead of returning true.
   */
  private async bridgeSaveAllImpl(
    extensionId: string,
    includeUntitled?: boolean,
  ): Promise<boolean> {
    if (!hasPermission(extensionId, "fs.write")) {
      throw new Error(
        `Extension "${extensionId}" cannot save files: requires permission "fs.write" not declared`,
      );
    }
    if (!this.options.onSaveAll) {
      throw new ExtensionApiUnsupportedError(
        "workspace.saveAll",
        "no editor save bridge is wired in this host",
      );
    }
    const result = await this.options.onSaveAll(includeUntitled);
    if (result.failedPaths.length > 0) {
      throw new Error(
        `workspace.saveAll failed for ${result.failedPaths.length} file(s): ${result.failedPaths.join(", ")}`,
      );
    }
    return true;
  }

  // -------------------------------------------------------------------------
  // F-6 (task-3.md): env API bridging
  //
  // - clipboard.readText/writeText: permission-gated by `clipboard`. Uses
  //   navigator.clipboard when available; falls back to a document-level
  //   execCommand copy / an in-memory buffer for jsdom.
  // - openExternal: permission-gated by `network` (opening a URL reaches
  //   the outside world). Uses window.open for http(s) URIs; file URIs are
  //   rejected (no shell.open in the browser host).
  // - machineId/sessionId: read-only properties generated in the ctor.
  // -------------------------------------------------------------------------

  /** Bridge vscode.env.clipboard.readText → navigator.clipboard. */
  private async bridgeClipboardReadTextImpl(
    extensionId: string,
  ): Promise<string> {
    if (!hasPermission(extensionId, "clipboard")) {
      throw new Error(
        `Extension "${extensionId}" cannot read clipboard: requires permission "clipboard" not declared`,
      );
    }
    if (typeof navigator !== "undefined" && navigator.clipboard?.readText) {
      try {
        return await navigator.clipboard.readText();
      } catch {
        // Permission denied or not available; fall through to fallback.
      }
    }
    return this.clipboardBuffers.get(extensionId) ?? "";
  }

  /** Bridge vscode.env.clipboard.writeText → navigator.clipboard. */
  private async bridgeClipboardWriteTextImpl(
    extensionId: string,
    value: string,
  ): Promise<void> {
    if (!hasPermission(extensionId, "clipboard")) {
      throw new Error(
        `Extension "${extensionId}" cannot write clipboard: requires permission "clipboard" not declared`,
      );
    }
    this.clipboardBuffers.set(extensionId, value);
    if (typeof navigator !== "undefined" && navigator.clipboard?.writeText) {
      try {
        await navigator.clipboard.writeText(value);
      } catch {
        // Permission denied; the in-memory buffer still holds the value.
      }
    }
  }

  /** Bridge vscode.env.openExternal → window.open. */
  private async bridgeOpenExternalImpl(
    extensionId: string,
    uri: Uri,
  ): Promise<boolean> {
    if (!hasPermission(extensionId, "env.openExternal")) {
      throw new Error(
        `Extension "${extensionId}" cannot open external URIs: requires permission "env.openExternal" not declared`,
      );
    }
    // Only http(s) URIs are opened in the browser host; file/mailto/etc.
    // are rejected because the browser cannot open them safely.
    if (uri.scheme !== "http" && uri.scheme !== "https") {
      this.notifyImpl(
        extensionId,
        "warn",
        `openExternal: scheme "${uri.scheme}" is not supported (only http/https)`,
      );
      return false;
    }
    await this.confirmRuntimeOperation("env.openExternal");
    this.assertActivePermission(extensionId, "env.openExternal", "open external URIs");
    const candidate = uri.path
      ? `${uri.scheme}://${uri.authority ?? ""}${uri.path}`
      : uri.fsPath;
    let parsed: URL;
    try {
      parsed = new URL(candidate);
    } catch {
      return false;
    }
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
      return false;
    }
    const url = parsed.href;
    try {
      const w = typeof window !== "undefined" ? window.open(url, "_blank") : null;
      return w !== null;
    } catch {
      return false;
    }
  }

  private assertActivePermission(
    extensionId: string,
    permission: ExtensionPermission,
    operation: string,
  ): void {
    if (!this.extensions.has(extensionId)) {
      throw new Error(
        `Extension "${extensionId}" cannot ${operation}: extension is no longer active or permission was revoked`,
      );
    }
    if (!hasPermission(extensionId, permission)) {
      throw new Error(
        `Extension "${extensionId}" cannot ${operation}: requires permission "${permission}" not declared`,
      );
    }
  }

  // -------------------------------------------------------------------------
  // F-6 (task-3.md): secrets API bridging.
  //
  // The renderer cannot authenticate an extension to the backend. Until a
  // trusted host exists, secret operations fail closed instead of exposing a
  // raw keyring account API or pretending sessionStorage is durable storage.
  // -------------------------------------------------------------------------

  /** Reject vscode.secrets.get until a trusted backend identity is available. */
  private async bridgeSecretGetImpl(
    extensionId: string,
    _key: string,
  ): Promise<string | undefined> {
    this.assertActivePermission(extensionId, "secrets.read", "read secrets");
    throw new Error(EXTENSION_SECRET_STORAGE_UNAVAILABLE);
  }

  /** Reject vscode.secrets.store until a trusted backend identity is available. */
  private async bridgeSecretStoreImpl(
    extensionId: string,
    _key: string,
    _value: string,
  ): Promise<void> {
    this.assertActivePermission(extensionId, "secrets.write", "store secrets");
    throw new Error(EXTENSION_SECRET_STORAGE_UNAVAILABLE);
  }

  /** Reject vscode.secrets.delete until a trusted backend identity is available. */
  private async bridgeSecretDeleteImpl(
    extensionId: string,
    _key: string,
  ): Promise<void> {
    this.assertActivePermission(extensionId, "secrets.write", "delete secrets");
    throw new Error(EXTENSION_SECRET_STORAGE_UNAVAILABLE);
  }

  // -------------------------------------------------------------------------
  // F-6 (task-3.md): window API 补齐 bridging
  //
  // - showInputBox/showQuickPick: best-effort v1 — return undefined (no
  //   host UI wired yet). A future iteration connects these to the host's
  //   QuickPick/InputBox Vue components.
  // - createOutputChannel: returns an in-memory OutputChannel backed by an
  //   array buffer. The host logs append/appendLine to the console; a
  //   future iteration wires it to the Output panel.
  // - createTerminal: permission-gated by `shell.execute`. Creates a
  //   Terminal backed by the backend TerminalService (startSession +
  //   writeSession). Tracked for disposal (killSession on dispose).
  // - registerTreeDataProvider: stores the provider keyed by viewId;
  //   returns a disposable. The host's TreeView component queries the
  //   provider via getChildren/getTreeItem.
  // - registerWebviewViewProvider: stores the provider keyed by viewId;
  //   returns a disposable. The host calls resolveWebviewView when the
  //   sidebar view becomes visible.
  // -------------------------------------------------------------------------

  /** Show an input box. G13: fail-closed until a real dialog is wired. */
  private async bridgeShowInputBoxImpl(
    _extensionId: string,
    _options?: InputBoxOptions,
  ): Promise<string | undefined> {
    throw new ExtensionApiUnsupportedError(
      "window.showInputBox",
      "no host input dialog is wired; returning a default value would be fake success",
    );
  }

  /** Show a quick pick. G13: fail-closed until a real picker is wired. */
  private async bridgeShowQuickPickImpl(
    _extensionId: string,
    _items: string[] | QuickPickItem[],
    _options?: QuickPickOptions,
  ): Promise<string | QuickPickItem | undefined> {
    throw new ExtensionApiUnsupportedError(
      "window.showQuickPick",
      "no host quick-pick dialog is wired; returning the first item would be fake success",
    );
  }

  /** Create an output channel backed by an in-memory buffer. */
  private bridgeCreateOutputChannelImpl(
    extensionId: string,
    name: string,
  ): OutputChannel {
    const lines: string[] = [];
    let visible = false;
    const channel: OutputChannel = {
      name,
      append(value: string) {
        lines.push(value);
      },
      appendLine(value: string) {
        lines.push(value);
      },
      clear() {
        lines.length = 0;
      },
      show(_preserveFocus?: boolean) {
        visible = true;
      },
      hide() {
        visible = false;
      },
      dispose() {
        lines.length = 0;
        visible = false;
      },
    };
    // Log to the console so output is visible even without a panel.
    const origAppendLine = channel.appendLine.bind(channel);
    channel.appendLine = (value: string) => {
      console.log(`[output:${name}] ${value}`);
      origAppendLine(value);
    };
    void visible; // suppress unused warning; tracked for future UI wiring.
    this.trackDisposable(extensionId, { dispose: () => channel.dispose() });
    return channel;
  }

  /**
   * Create a terminal. Requires `shell.execute` permission. Backed by
   * the backend TerminalService (startSession + writeSession). The
   * terminal is tracked for disposal (killSession on dispose).
   *
   * Synchronous (mirrors VS Code's createTerminal): the session is
   * started in the background; sendText is buffered until the session
   * is ready.
   */
  private bridgeCreateTerminalImpl(
    extensionId: string,
    options?: TerminalOptions,
  ): Terminal {
    if (!hasPermission(extensionId, "shell.execute")) {
      throw new Error(
        `Extension "${extensionId}" cannot create a terminal: requires permission "shell.execute" not declared`,
      );
    }
    const sessionId = `ext-${encodeURIComponent(extensionId)}-${randomHexId(16)}`;
    let disposed = false;
    const pendingWrites: string[] = [];
    let sessionReady = false;
    let sessionFailed = false;
    let sessionStarted = false;
    const markUnavailable = () => {
      sessionFailed = true;
      sessionReady = false;
      pendingWrites.length = 0;
    };
    const pendingWritesDisposable: Disposable = {
      dispose() {
        pendingWrites.length = 0;
        sessionReady = false;
      },
    };
    this.trackDisposable(extensionId, pendingWritesDisposable);

    // createTerminal is synchronous, so retain one confirmation promise for
    // session startup and every subsequent write. No backend module is loaded
    // until approval succeeds.
    const confirmation = this.confirmRuntimeOperation("window.createTerminal");
    void confirmation
      .then(async () => {
        if (disposed) return;
        const { terminalService } = await loadTerminalServices();
        const { appState } = await import("@/stores/app");
        if (disposed) return;
        const cwd = options?.cwd ?? appState.currentProject ?? "";
        const shell = options?.shellPath ?? "";
        await terminalService.startSession(sessionId, cwd, shell);
        sessionStarted = true;
        if (disposed) {
          await terminalService.killSession(sessionId).catch(() => {
            // Session may already have been killed by dispose().
          });
          return;
        }
        sessionReady = true;
        // Flush any pending writes.
        const writes = pendingWrites.splice(0, pendingWrites.length);
        for (const w of writes) {
          void terminalService.writeSession(sessionId, w).catch(() => {
            // ignore
          });
        }
      })
      .catch((error: unknown) => {
        markUnavailable();
        const message = error instanceof Error ? error.message : String(error);
        this.notifyImpl(
          extensionId,
          "error",
          `Failed to start terminal session "${sessionId}": ${message}`,
        );
      });
    const terminal: Terminal = {
      name: options?.name ?? "Extension Terminal",
      sendText(text: string, addNewLine = true) {
        if (disposed || sessionFailed) return;
        const payload = addNewLine ? `${text}\n` : text;
        if (sessionReady) {
          void confirmation
            .then(async () => {
              if (disposed || sessionFailed || !sessionReady) return;
              const { terminalService } = await loadTerminalServices();
              if (disposed || sessionFailed || !sessionReady) return;
              await terminalService.writeSession(sessionId, payload);
            })
            .catch(() => {
              markUnavailable();
            });
        } else {
          pendingWrites.push(payload);
        }
      },
      show() {
        // v1: no-op (no terminal view switching wired yet).
      },
      hide() {
        // v1: no-op.
      },
      dispose() {
        if (disposed) return;
        disposed = true;
        pendingWritesDisposable.dispose();
        if (!sessionStarted) return;
        void loadTerminalServices()
          .then(({ terminalService }) => terminalService.killSession(sessionId))
          .catch(() => {
            // ignore
          });
      },
    };
    this.trackDisposable(extensionId, { dispose: () => terminal.dispose() });
    return terminal;
  }

  /**
   * Register a tree data provider for a view id. Returns a disposable.
   * The host's TreeView component queries the provider via
   * getChildren/getTreeItem when the view is rendered.
   */
  private bridgeRegisterTreeDataProviderImpl<T>(
    extensionId: string,
    viewId: string,
    treeDataProvider: TreeDataProvider<T>,
  ): Disposable {
    const existing = this.treeViewProviders.get(viewId);
    if (existing && existing.extensionId !== extensionId) {
      throw new Error(
        `Tree data provider view "${viewId}" is already registered by extension "${existing.extensionId}"`,
      );
    }
    const registration = {
      extensionId,
      provider: treeDataProvider as TreeDataProvider<unknown>,
    };
    this.treeViewProviders.set(viewId, registration);
    const disposable: Disposable = {
      dispose: () => {
        if (this.treeViewProviders.get(viewId) === registration) {
          this.treeViewProviders.delete(viewId);
        }
      },
    };
    this.trackDisposable(extensionId, disposable);
    return disposable;
  }

  /**
   * Register a webview view provider for a view id. Returns a disposable.
   * The host calls resolveWebviewView when the sidebar view becomes
   * visible.
   */
  private bridgeRegisterWebviewViewProviderImpl(
    extensionId: string,
    viewId: string,
    provider: WebviewViewProvider,
  ): Disposable {
    if (!hasPermission(extensionId, "ui.webview")) {
      throw new Error(
        `Extension "${extensionId}" cannot register webview view providers: requires permission "ui.webview" not declared`,
      );
    }
    const existing = this.webviewViewProviders.get(viewId);
    if (existing && existing.extensionId !== extensionId) {
      throw new Error(
        `Webview view provider "${viewId}" is already registered by extension "${existing.extensionId}"`,
      );
    }
    const registration = { extensionId, provider };
    this.webviewViewProviders.set(viewId, registration);
    const disposable: Disposable = {
      dispose: () => {
        if (this.webviewViewProviders.get(viewId) === registration) {
          this.webviewViewProviders.delete(viewId);
        }
      },
    };
    this.trackDisposable(extensionId, disposable);
    return disposable;
  }

  // -------------------------------------------------------------------------
  // Bridge factory
  // -------------------------------------------------------------------------

  /**
   * Build the VscodeHostBridge for an extension. Each method closes over
   * the extension id so the vscode API shim can call back into the host
   * without the extension needing to pass its own id.
   */
  private createBridge(
    desc: ExtensionDescriptor & { permissions: ExtensionPermission[] },
  ): VscodeHostBridge {
    const extensionId = desc.id;
    const permissions = desc.permissions;
    return {
      extensionId,
      permissions,
      trackDisposable: (d: Disposable) => {
        this.trackDisposable(extensionId, d);
      },
      // BUG-FIX-2d: 桥接活跃编辑器状态，解决 vscodeApi.ts 中
      // activeTextEditor 始终为 undefined 的问题。
      bridgeGetActiveTextEditor: () =>
        this.options.onGetActiveTextEditor?.() as
          | {
              document: TextDocument;
              selection?: unknown;
              // eslint-disable-next-line @typescript-eslint/no-explicit-any
              [key: string]: any;
            }
          | undefined,
      // BUG-FIX-2d: 桥接扩展配置，解决 getConfiguration 始终返回空快照的问题。
      bridgeGetConfiguration: (section?: string) =>
        this.options.onGetConfiguration?.(section) ?? {},
      registerCommand: (command: string, cb: (...args: unknown[]) => unknown) =>
        this.registerCommandImpl(
          extensionId,
          command,
          cb,
        ),
      executeCommand: (command: string, ...args: unknown[]) =>
        this.executeCommand(command, ...args),
      isCommandAllowed: (command: string) =>
        this.commands.get(command)?.extensionId === extensionId,
      bridgeLanguageProvider: (
        kind: LanguageProviderKind,
        selector: DocumentSelector,
        provider: unknown,
        extra?: unknown,
      ) => this.bridgeLanguageProviderImpl(extensionId, kind, selector, provider, extra),
      bridgeReadFile: (uri: Uri) => this.bridgeReadFileImpl(extensionId, uri),
      bridgeWriteFile: (uri: Uri, content: Uint8Array) =>
        this.bridgeWriteFileImpl(extensionId, uri, content),
      bridgeExists: (uri: Uri) => this.bridgeExistsImpl(extensionId, uri),
      bridgeCreateDirectory: (uri: Uri) =>
        this.bridgeCreateDirectoryImpl(extensionId, uri),
      // F-6 (task-3.md): workspace.fs 补齐 bridge wiring.
      bridgeRename: (
        oldUri: Uri,
        newUri: Uri,
        options?: { overwrite?: boolean },
      ) => this.bridgeRenameImpl(extensionId, oldUri, newUri, options),
      bridgeDelete: (
        uri: Uri,
        options?: { recursive?: boolean; useTrash?: boolean },
      ) => this.bridgeDeleteImpl(extensionId, uri, options),
      bridgeReadDirectory: (uri: Uri) =>
        this.bridgeReadDirectoryImpl(extensionId, uri),
      // F-6 (task-3.md): workspace API 补齐 bridge wiring.
      bridgeFindFiles: (
        include: GlobPattern,
        exclude: GlobPattern | undefined,
        maxResults: number | undefined,
      ) => this.bridgeFindFilesImpl(extensionId, include, exclude, maxResults),
      bridgeFindTextInFiles: (
        query: TextSearchQuery,
        options: FindTextInFilesOptions,
      ) => this.bridgeFindTextInFilesImpl(extensionId, query, options),
      bridgeOpenTextDocument: (uri: Uri) =>
        this.bridgeOpenTextDocumentImpl(extensionId, uri),
      bridgeSaveAll: (includeUntitled?: boolean) =>
        this.bridgeSaveAllImpl(extensionId, includeUntitled),
      bridgeOnDidSaveTextDocument: (listener: (e: TextDocument) => void) =>
        this.trackDisposable(
          extensionId,
          this.onDidSaveTextDocumentEmitter.event(listener),
        ),
      bridgeOnDidChangeTextDocument: (
        listener: (e: TextDocumentChangeEvent) => void,
      ) =>
        this.trackDisposable(
          extensionId,
          this.onDidChangeTextDocumentEmitter.event(listener),
        ),
      bridgeOnDidOpenTextDocument: (listener: (e: TextDocument) => void) =>
        this.trackDisposable(
          extensionId,
          this.onDidOpenTextDocumentEmitter.event(listener),
        ),
      createWebviewPanel: (
        viewType: string,
        title: string,
        showOptions: unknown,
        options?: unknown,
      ) =>
        this.createWebviewPanelImpl(
          extensionId,
          viewType,
          title,
          showOptions,
          options,
        ),
      notify: (level: "info" | "warn" | "error", message: string) =>
        this.notifyExtensionImpl(extensionId, level, message),
      // F-6 (task-3.md): tasks API bridge wiring.
      bridgeRegisterTaskProvider: (type: string, provider: TaskProvider) =>
        this.bridgeRegisterTaskProviderImpl(extensionId, type, provider),
      bridgeExecuteTask: (task: Task) =>
        this.bridgeExecuteTaskImpl(extensionId, task),
      bridgeFetchTasks: () => this.bridgeFetchTasksImpl(extensionId),
      // F-6 (task-3.md): debug API bridge wiring.
      bridgeRegisterDebugConfigurationProvider: (
        type: string,
        provider: DebugConfigurationProvider,
      ) => this.bridgeRegisterDebugConfigurationProviderImpl(extensionId, type, provider),
      bridgeStartDebugging: (
        folder: WorkspaceFolder | undefined,
        config: DebugConfiguration,
      ) => this.bridgeStartDebuggingImpl(extensionId, folder, config),
      // F-6 (task-3.md): scm API bridge wiring.
      bridgeCreateSourceControl: (
        id: string,
        label: string,
        rootUri: Uri | undefined,
      ) => this.bridgeCreateSourceControlImpl(extensionId, id, label, rootUri),
      // F-6 (task-3.md): window API 补齐 bridge wiring.
      bridgeShowInputBox: (options?: InputBoxOptions) =>
        this.bridgeShowInputBoxImpl(extensionId, options),
      bridgeShowQuickPick: (
        items: string[] | QuickPickItem[],
        options?: QuickPickOptions,
      ) => this.bridgeShowQuickPickImpl(extensionId, items, options),
      bridgeCreateOutputChannel: (name: string) =>
        this.bridgeCreateOutputChannelImpl(extensionId, name),
      bridgeCreateTerminal: (options?: TerminalOptions) =>
        this.bridgeCreateTerminalImpl(extensionId, options),
      bridgeRegisterTreeDataProvider: <T>(
        viewId: string,
        treeDataProvider: TreeDataProvider<T>,
      ) =>
        this.bridgeRegisterTreeDataProviderImpl(
          extensionId,
          viewId,
          treeDataProvider,
        ),
      bridgeRegisterWebviewViewProvider: (
        viewId: string,
        provider: WebviewViewProvider,
      ) =>
        this.bridgeRegisterWebviewViewProviderImpl(extensionId, viewId, provider),
      // F-6 (task-3.md): env API bridge wiring.
      bridgeClipboardReadText: () => this.bridgeClipboardReadTextImpl(extensionId),
      bridgeClipboardWriteText: (value: string) =>
        this.bridgeClipboardWriteTextImpl(extensionId, value),
      bridgeOpenExternal: (uri: Uri) => this.bridgeOpenExternalImpl(extensionId, uri),
      machineId: this.machineId,
      sessionId: this.sessionId,
      // F-6 (task-3.md): secrets API bridge wiring.
      bridgeSecretGet: (key: string) => this.bridgeSecretGetImpl(extensionId, key),
      bridgeSecretStore: (key: string, value: string) =>
        this.bridgeSecretStoreImpl(extensionId, key, value),
      bridgeSecretDelete: (key: string) =>
        this.bridgeSecretDeleteImpl(extensionId, key),
    };
  }
}
