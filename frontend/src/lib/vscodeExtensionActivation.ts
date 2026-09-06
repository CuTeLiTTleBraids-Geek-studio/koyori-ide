/**
 * F-3 (prompt-2.md): VS Code 扩展激活编排器。
 *
 * 职责：
 *   - 加载所有已安装扩展的 manifest（含 ParsedContributes）
 *   - 在对应时机触发 activationEvents：
 *     · onLanguage:<lang>   — editor.ts:openFile 打开文件时
 *     · onCommand:<cmd>     — vscodeExtensions.ts:executeVscodeExtensionCommand 执行命令前
 *     · workspaceContains:<glob> — app.ts:openProject 打开工作区时
 *     · onDebug / onDebugResolve:<type> — debugService 启动调试时
 *     · *                   — App.vue 启动时（eager，需用户确认）
 *   - 加载 manifest 时把 contributes.commands 注入 vscodeExtensions.ts 命令 registry
 *   - 激活扩展时把 contributes.views 注入 vscodeExtensions.ts 视图 registry
 *
 * 设计：本模块根据 activationEvents 决定何时激活，先注入静态 contributes，
 * 再通过单例 ExtensionHost 启动扩展主模块。扩展源码只在专用 Worker 中运行；
 * 主 WebView 只保留经 ExtensionHost 权限门控的命令 RPC。
 */
// Koyori IDE 模块 · Vscode Extension Activation；交互服务：调试（DebugService）、插件市场（MarketplaceService）。
// 喵，这是 Koyori IDE 的 Vscode Extension Activation 模块（前端实现）~

import * as monaco from "monaco-editor";
import { marketplaceService } from "@/api/services";
import { restoreBuiltInMonacoTheme } from "@/lib/monaco-themes";
import type { VSCodeExtensionManifest, ExtensionContributes, ExtensionCommandContribution, ExtensionViewContribution, ExtensionGrammarContribution, ExtensionSnippetContribution } from "@/types";
import {
  ExtensionApiUnsupportedError,
  ExtensionHost,
  resetExtensionHostModuleState,
  type ExtensionModule,
  type MonacoBridge,
} from "@/lib/extensionHost/extensionHost";
import type { ExtensionPermission } from "@/lib/extensionHost/permissions";
import type {
  DebugConfiguration,
  DocumentSelector,
  Disposable,
  FindTextInFilesOptions,
  GlobPattern,
  LanguageProviderKind,
  OutputChannel,
  SourceControl,
  SourceControlResourceGroup,
  StatusBarItem,
  FileSystemWatcher,
  Task,
  TaskExecution,
  Terminal,
  TerminalOptions,
  TextEditor,
  TextEditorDecorationType,
  DecorationRenderOptions,
  TextSearchQuery,
  TextDocument,
  Uri,
  VscodeAPI,
  WebviewPanel,
  InputBoxOptions,
  QuickPickItem,
  QuickPickOptions,
  Progress,
  ProgressOptions,
  Thenable,
  WorkspaceFolder,
} from "@/lib/extensionHost/vscodeApi";
import {
  getExtensionSecurityInfo,
  loadExtensionSecurityInfos,
  refreshExtensionSecurityInfo,
  removeExtensionSecurityInfo,
} from "@/stores/extensionSecurity";
import { translate } from "@/lib/i18n";
import {
  registerVscodeExtensionCommand,
  listVscodeExtensionCommands,
  unregisterVscodeExtensionCommand,
  registerVscodeExtensionViews,
  unregisterVscodeExtensionViews,
  listVscodeExtensionViews,
  registerVscodeExtensionGrammars,
  unregisterVscodeExtensionGrammars,
  registerVscodeExtensionSnippets,
  unregisterVscodeExtensionSnippets,
  registerVscodeExtensionThemes,
  unregisterVscodeExtensionThemes,
  getActiveVscodeExtensionTheme,
} from "@/lib/vscodeExtensions";

// ---------------------------------------------------------------------------
// 状态
// ---------------------------------------------------------------------------

interface ActivationState {
  manifestCache: Map<string, VSCodeExtensionManifest>;
  activatedExtensions: Set<string>;
  injectedExtensions: Set<string>;
  activationErrors: Map<string, Error>;
  activationPromises: Map<string, Promise<boolean>>;
  deactivationPromises: Map<string, Promise<void>>;
  deactivationRequested: Set<string>;
  moduleSourceCache: Map<string, { mainPath: string; source: string }>;
  workerModules: Set<WorkerExtensionModule>;
  workerRestartAttempts: Map<string, number>;
  workerRecoveryPromises: Map<string, Promise<void>>;
  workerRecoveryGeneration: Map<string, number>;
  lifecycleHolds: Set<string>;
  extensionHost?: ExtensionHost;
  securityLoadPromise?: Promise<void>;
  manifestsLoaded: boolean;
  resetting: boolean;
}

let activationState: ActivationState | undefined;

interface ExtensionActivationRuntimeGlobal {
  __koyoriIdeExtensionHostTeardown?: Promise<void>;
}

const activationRuntimeGlobal =
  globalThis as unknown as ExtensionActivationRuntimeGlobal;

async function waitForPriorExtensionHostTeardown(): Promise<void> {
  const teardown = activationRuntimeGlobal.__koyoriIdeExtensionHostTeardown;
  if (teardown) await teardown;
}

/** Lazily allocate state so reset can detach in-flight work from fresh state. */
function getActivationState(): ActivationState {
  if (!activationState) {
    activationState = {
      manifestCache: new Map<string, VSCodeExtensionManifest>(),
      activatedExtensions: new Set<string>(),
      injectedExtensions: new Set<string>(),
      activationErrors: new Map<string, Error>(),
      activationPromises: new Map<string, Promise<boolean>>(),
      deactivationPromises: new Map<string, Promise<void>>(),
      deactivationRequested: new Set<string>(),
      moduleSourceCache: new Map<string, { mainPath: string; source: string }>(),
      workerModules: new Set<WorkerExtensionModule>(),
      workerRestartAttempts: new Map<string, number>(),
      workerRecoveryPromises: new Map<string, Promise<void>>(),
      workerRecoveryGeneration: new Map<string, number>(),
      lifecycleHolds: new Set<string>(),
      manifestsLoaded: false,
      resetting: false,
    };
  }
  return activationState;
}

type WorkerModuleStatus =
  | "idle"
  | "activating"
  | "active"
  | "deactivating"
  | "disposed";

interface ExtensionPackageJSON {
  browser?: unknown;
  main?: unknown;
}

const EXTENSION_PACKAGE_PATH = "extension/package.json";
const WORKER_ACTIVATION_TIMEOUT_MS = 15_000;
const WORKER_DEACTIVATION_TIMEOUT_MS = 10_000;
const ACTIVATION_RESET_GRACE_MS = 250;
const MAX_EXTENSION_WORKER_RESTARTS = 3;
export const EXTENSION_WORKER_PROTOCOL_VERSION = "1.0";
const EXTENSION_WORKER_PROTOCOL_VERSIONS = Object.freeze([
  EXTENSION_WORKER_PROTOCOL_VERSION,
] as const);
const WORKER_HEALTH_CHECK_INTERVAL_MS = 2_000;
const WORKER_HEALTH_CHECK_TIMEOUT_MS = 8_000;
const WORKER_MAX_MESSAGE_BYTES = 4 * 1024 * 1024;
const WORKER_MAX_MESSAGES_PER_SECOND = 1_000;

export interface ExtensionWorkerRuntimePolicy {
  offeredProtocolVersions?: readonly string[];
  healthCheckIntervalMs?: number;
  healthCheckTimeoutMs?: number;
  maxMessageBytes?: number;
  maxMessagesPerSecond?: number;
}

interface NormalizedExtensionWorkerRuntimePolicy {
  offeredProtocolVersions: readonly string[];
  healthCheckIntervalMs: number;
  healthCheckTimeoutMs: number;
  maxMessageBytes: number;
  maxMessagesPerSecond: number;
}

function normalizePositiveInteger(
  value: number | undefined,
  fallback: number,
  label: string,
): number {
  const normalized = value ?? fallback;
  if (!Number.isSafeInteger(normalized) || normalized <= 0) {
    throw new Error(`${label} must be a positive safe integer`);
  }
  return normalized;
}

function normalizeExtensionWorkerRuntimePolicy(
  policy: ExtensionWorkerRuntimePolicy = {},
): NormalizedExtensionWorkerRuntimePolicy {
  const offeredProtocolVersions = policy.offeredProtocolVersions
    ? Array.from(new Set(policy.offeredProtocolVersions))
    : [...EXTENSION_WORKER_PROTOCOL_VERSIONS];
  if (
    offeredProtocolVersions.length === 0
    || offeredProtocolVersions.some(
      (version) => typeof version !== "string" || !/^\d+\.\d+$/.test(version),
    )
  ) {
    throw new Error("Extension Worker protocol offers must be non-empty major.minor versions");
  }
  return Object.freeze({
    offeredProtocolVersions: Object.freeze(offeredProtocolVersions),
    healthCheckIntervalMs: normalizePositiveInteger(
      policy.healthCheckIntervalMs,
      WORKER_HEALTH_CHECK_INTERVAL_MS,
      "Extension Worker health-check interval",
    ),
    healthCheckTimeoutMs: normalizePositiveInteger(
      policy.healthCheckTimeoutMs,
      WORKER_HEALTH_CHECK_TIMEOUT_MS,
      "Extension Worker health-check timeout",
    ),
    maxMessageBytes: normalizePositiveInteger(
      policy.maxMessageBytes,
      WORKER_MAX_MESSAGE_BYTES,
      "Extension Worker message-size limit",
    ),
    maxMessagesPerSecond: normalizePositiveInteger(
      policy.maxMessagesPerSecond,
      WORKER_MAX_MESSAGES_PER_SECOND,
      "Extension Worker message-rate limit",
    ),
  });
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function boundedStructuredSize(value: unknown, limit: number): number {
  const pending: unknown[] = [value];
  const seen = new WeakSet<object>();
  let total = 0;
  while (pending.length > 0 && total <= limit) {
    const entry = pending.pop();
    if (entry === null || entry === undefined) {
      total += 4;
    } else if (typeof entry === "string") {
      total += entry.length * 2;
    } else if (
      typeof entry === "number"
      || typeof entry === "bigint"
      || typeof entry === "boolean"
    ) {
      total += 8;
    } else if (typeof entry === "object") {
      if (seen.has(entry)) continue;
      seen.add(entry);
      if (entry instanceof ArrayBuffer) {
        total += entry.byteLength;
      } else if (ArrayBuffer.isView(entry)) {
        total += entry.byteLength;
      } else if (typeof Blob !== "undefined" && entry instanceof Blob) {
        total += entry.size;
      } else if (entry instanceof Map) {
        total += entry.size * 8;
        for (const [key, mapValue] of entry) pending.push(key, mapValue);
      } else if (entry instanceof Set) {
        total += entry.size * 4;
        for (const setValue of entry) pending.push(setValue);
      } else {
        for (const [key, objectValue] of Object.entries(entry)) {
          total += key.length * 2;
          pending.push(objectValue);
        }
      }
    }
  }
  return total;
}

async function waitForActivationResetGrace(
  work: PromiseLike<unknown>,
): Promise<boolean> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<boolean>((resolve) => {
    timer = setTimeout(() => resolve(false), ACTIVATION_RESET_GRACE_MS);
  });
  try {
    return await Promise.race([
      Promise.resolve(work).then(() => true),
      timeout,
    ]);
  } finally {
    if (timer !== undefined) clearTimeout(timer);
  }
}

function isStringArray(value: unknown): value is string[] {
  return Array.isArray(value) && value.every((item) => typeof item === "string");
}

function isWorkerTask(value: unknown): value is Task {
  if (!isRecord(value) || !isRecord(value.definition) || !isRecord(value.execution)) {
    return false;
  }
  return (
    typeof value.name === "string" &&
    typeof value.source === "string" &&
    typeof value.definition.type === "string" &&
    typeof value.execution.command === "string" &&
    (value.execution.args === undefined || isStringArray(value.execution.args)) &&
    (value.execution.cwd === undefined || typeof value.execution.cwd === "string") &&
    (value.execution.shell === undefined || typeof value.execution.shell === "boolean")
  );
}

function isTerminalOptions(value: unknown): value is TerminalOptions {
  if (value === undefined) return true;
  if (!isRecord(value)) return false;
  return (
    (value.name === undefined || typeof value.name === "string") &&
    (value.cwd === undefined || typeof value.cwd === "string") &&
    (value.shellPath === undefined || typeof value.shellPath === "string") &&
    (value.shellArgs === undefined || isStringArray(value.shellArgs))
  );
}

function normalizeWorkerBytes(value: unknown): Uint8Array | undefined {
  if (value instanceof Uint8Array) return value;
  if (
    ArrayBuffer.isView(value)
    && Object.prototype.toString.call(value) === "[object Uint8Array]"
  ) {
    return new Uint8Array(value.buffer, value.byteOffset, value.byteLength);
  }
  return undefined;
}

function isWorkerUri(value: unknown): value is Uri {
  return isRecord(value)
    && typeof value.scheme === "string"
    && typeof value.fsPath === "string";
}

function isWorkerPosition(value: unknown): boolean {
  return isRecord(value)
    && Number.isInteger(value.line)
    && Number(value.line) >= 0
    && Number.isInteger(value.character)
    && Number(value.character) >= 0;
}

function isWorkerRange(value: unknown): boolean {
  return isRecord(value)
    && isWorkerPosition(value.start)
    && isWorkerPosition(value.end);
}

function isWorkerSelection(value: unknown): boolean {
  return isWorkerRange(value)
    && isWorkerPosition((value as Record<string, unknown>).anchor)
    && isWorkerPosition((value as Record<string, unknown>).active);
}

function isDocumentFilter(value: unknown): value is Exclude<DocumentSelector, readonly unknown[]> {
  return isRecord(value)
    && typeof value.language === "string"
    && (value.scheme === undefined || typeof value.scheme === "string")
    && (value.pattern === undefined || typeof value.pattern === "string");
}

function isDocumentSelector(value: unknown): value is DocumentSelector {
  return Array.isArray(value)
    ? value.length > 0 && value.every(isDocumentFilter)
    : isDocumentFilter(value);
}

function isCallbackMap(value: unknown): value is Record<string, number> {
  return isRecord(value)
    && Object.values(value).every((callbackId) => typeof callbackId === "number");
}

function isGlobPattern(value: unknown): value is GlobPattern {
  return typeof value === "string"
    || (isRecord(value)
      && typeof value.base === "string"
      && typeof value.pattern === "string");
}

const LANGUAGE_PROVIDER_KINDS = new Set<LanguageProviderKind>([
  "completion",
  "hover",
  "definition",
  "codeAction",
  "reference",
  "codeLens",
  "documentFormatting",
  "documentRangeFormatting",
  "onTypeFormatting",
  "signatureHelp",
  "workspaceSymbol",
  "documentLink",
  "color",
  "foldingRange",
  "declaration",
  "implementation",
  "typeDefinition",
  "rename",
  "documentSymbol",
  "documentSemanticTokens",
  "documentRangeSemanticTokens",
  "documentHighlight",
  "inlayHints",
]);

function toWorkerSerializable(
  value: unknown,
  seen = new WeakSet<object>(),
): unknown {
  if (
    value === null
    || value === undefined
    || typeof value === "string"
    || typeof value === "number"
    || typeof value === "boolean"
    || typeof value === "bigint"
  ) {
    return value;
  }
  if (value instanceof Uint8Array) return value;
  if (Array.isArray(value)) {
    return value.map((entry) => toWorkerSerializable(entry, seen));
  }
  if (typeof value !== "object") return undefined;
  if (seen.has(value)) return undefined;
  seen.add(value);

  const candidate = value as Record<string, unknown>;
  const editorDocument = candidate.document;
  if (
    isRecord(editorDocument)
    && typeof editorDocument.getText === "function"
    && typeof candidate.setDecorations === "function"
  ) {
    return {
      __koyoriIdeType: "TextEditor",
      document: toWorkerSerializable(editorDocument, seen),
      selection: toWorkerSerializable(candidate.selection, seen),
    };
  }
  const getText = candidate.getText;
  const getValue = candidate.getValue;
  const uri = candidate.uri;
  if (
    (typeof getText === "function" || typeof getValue === "function")
    && isRecord(uri)
  ) {
    let text = "";
    if (typeof getText === "function") {
      text = String(getText.call(value));
    } else if (typeof getValue === "function") {
      text = String(getValue.call(value));
    }
    const languageId = typeof candidate.languageId === "string"
      ? candidate.languageId
      : typeof candidate.getLanguageId === "function"
        ? String(candidate.getLanguageId.call(value))
        : "plaintext";
    const version = typeof candidate.version === "number"
      ? candidate.version
      : typeof candidate.getVersionId === "function"
        ? Number(candidate.getVersionId.call(value))
        : 1;
    const fileName = typeof candidate.fileName === "string"
      ? candidate.fileName
      : typeof (uri as Record<string, unknown>).fsPath === "string"
        ? String((uri as Record<string, unknown>).fsPath)
        : undefined;
    const lineCount = typeof candidate.lineCount === "number"
      ? candidate.lineCount
      : text.split(/\r\n|\r|\n/).length;
    return {
      __koyoriIdeType: "TextDocument",
      uri: toWorkerSerializable(uri, seen),
      fileName,
      languageId,
      version,
      lineCount,
      text,
    };
  }

  const result: Record<string, unknown> = Object.create(null);
  for (const [key, entry] of Object.entries(candidate)) {
    if (typeof entry === "function") continue;
    const serialized = toWorkerSerializable(entry, seen);
    if (serialized !== undefined) result[key] = serialized;
  }
  return result;
}

function serializeTextDocument(document: TextDocument): unknown {
  const text = document.getText();
  const fileName = document.fileName ?? document.uri.fsPath;
  const lineCount = document.lineCount ?? text.split(/\r\n|\r|\n/).length;
  return {
    __koyoriIdeType: "TextDocument",
    uri: toWorkerSerializable(document.uri),
    fileName,
    languageId: document.languageId,
    lineCount,
    text,
  };
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function decodeExtensionFile(contents: Uint8Array): string {
  return new TextDecoder().decode(contents);
}

function extensionWorkerBootstrap(): void {
  type WorkerScope = {
    postMessage(message: unknown): void;
    addEventListener(
      type: "message",
      listener: (event: MessageEvent<unknown>) => void,
    ): void;
    addEventListener(
      type: "error",
      listener: (event: { message?: unknown }) => void,
    ): void;
    close(): void;
  };

  const scope = globalThis as unknown as WorkerScope & Record<string, unknown>;
  const send = scope.postMessage.bind(scope);
  let protocolToken = "";
  const supportedProtocolVersions = Object.freeze(["1.0"]);
  type LoadedExtensionModule = {
    activate(context: unknown): unknown;
    deactivate?: () => unknown;
  };
  type ExtensionSubscription = { dispose(): unknown };
  let extensionModule: LoadedExtensionModule | undefined;
  const extensionSubscriptions: ExtensionSubscription[] = [];
  // Runtime registrations remain legal until deactivation. The separate
  // activation flag only controls which promises belong to the initial
  // activation barrier.
  let acceptingRegistrations = true;
  let activationInProgress = true;
  let hostMachineId = "";
  let hostSessionId = "";
  const configurationSnapshots = new Map<string, Record<string, unknown>>();
  let activeTextEditorSnapshot: unknown;
  let workspaceFoldersSnapshot: unknown;
  let nextRequestId = 1;
  let nextCallbackId = 1;
  const pendingHostRequests = new Map<
    number,
    { resolve(value: unknown): void; reject(error: Error): void }
  >();
  const callbacks = new Map<number, (...args: unknown[]) => unknown>();
  const taskCompletionHandlers = new Map<number, () => void>();
  const pendingProgressReports = new Map<number, Set<Promise<unknown>>>();
  const decorationHandles = new WeakMap<object, Promise<number>>();
  let nextDecorationTypeKey = 1;
  const registrations: Promise<unknown>[] = [];
  const isObjectRecord = (
    value: unknown,
  ): value is Record<string, unknown> =>
    typeof value === "object" && value !== null;

  const toErrorMessage = (error: unknown): string =>
    error instanceof Error ? error.message : String(error);

  const unsupportedApi = (api: string): Error =>
    new Error(
      "KOYORI_IDE_EXT_API_UNSUPPORTED: " +
        api +
        " is not implemented (koyori-ide extension API v1)",
    );

  const post = (type: string, payload: Record<string, unknown> = {}): void => {
    send({ type, token: protocolToken, ...payload });
  };

  // Some WebView2 versions report an uncaught exception to the Worker global
  // without forwarding it to the owning Worker object's `onerror` property.
  // Bridge it through the authenticated protocol before closing the runtime.
  const handleWorkerError = (event: { message?: unknown }): void => {
    post("runtime-error", {
      error: typeof event.message === "string" ? event.message : "Extension Worker crashed",
    });
    scope.close();
  };
  scope.addEventListener("error", handleWorkerError);

  const requestHost = (method: string, args: unknown[]): Promise<unknown> => {
    const id = nextRequestId++;
    return new Promise((resolve, reject) => {
      pendingHostRequests.set(id, { resolve, reject });
      try {
        post("rpc", { id, method, args });
      } catch (error) {
        pendingHostRequests.delete(id);
        reject(new Error(toErrorMessage(error)));
      }
    });
  };

  const reviveWorkerValue = (value: unknown): unknown => {
    if (Array.isArray(value)) return value.map(reviveWorkerValue);
    if (!isObjectRecord(value)) return value;
    if (value.__koyoriIdeType === "Progress" && typeof value.id === "number") {
      const progressId = value.id;
      return Object.freeze({
        report(reportValue: unknown): Promise<unknown> {
          const request = requestHost("window.progress.report", [progressId, reportValue])
            .catch(() => undefined);
          let reports = pendingProgressReports.get(progressId);
          if (!reports) {
            reports = new Set<Promise<unknown>>();
            pendingProgressReports.set(progressId, reports);
          }
          reports.add(request);
          void request.finally(() => reports?.delete(request));
          return request;
        },
      });
    }
    if (value.__koyoriIdeType === "ConfigurationChangeEvent") {
      const section = typeof value.section === "string" ? value.section : undefined;
      if (isObjectRecord(value.configuration)) {
        configurationSnapshots.set(section ?? "", value.configuration);
      }
      return Object.freeze({
        affectsConfiguration(candidate: string): boolean {
          if (!section) return true;
          return candidate === section
            || candidate.startsWith(section + ".")
            || section.startsWith(candidate + ".");
        },
      });
    }
    if (value.__koyoriIdeType === "TextDocument") {
      const text = typeof value.text === "string" ? value.text : "";
      const lines = text.split(/\r\n|\r|\n/);
      const document = { ...value };
      delete document.__koyoriIdeType;
      delete document.text;
      return Object.freeze({
        ...document,
        fileName: typeof document.fileName === "string"
          ? document.fileName
          : isObjectRecord(document.uri) && typeof document.uri.fsPath === "string"
            ? document.uri.fsPath
            : "",
        lineCount: typeof document.lineCount === "number"
          ? document.lineCount
          : lines.length,
        getText(): string {
          return text;
        },
        lineAt(line: number): { text: string } {
          if (!Number.isInteger(line) || line < 0 || line >= lines.length) {
            throw new Error("TextDocument.lineAt line is out of range");
          }
          return { text: lines[line] };
        },
      });
    }
    if (value.__koyoriIdeType === "TextEditor") {
      const document = reviveWorkerValue(value.document);
      let currentSelection = value.selection === undefined
        ? undefined
        : reviveWorkerValue(value.selection);
      const documentUri = isObjectRecord(value.document)
        ? value.document.uri
        : undefined;
      const editor = {
        document,
        get selection(): unknown {
          return currentSelection;
        },
        set selection(next: unknown) {
          currentSelection = next;
          void requestHost("window.textEditor.setSelection", [next, documentUri]).catch((error) => {
            post("runtime-error", { error: toErrorMessage(error) });
          });
        },
        revealRange(range: unknown, revealType = 0): void {
          void requestHost("window.textEditor.revealRange", [range, revealType, documentUri]).catch((error) => {
            post("runtime-error", { error: toErrorMessage(error) });
          });
        },
        setDecorations(type: unknown, ranges: unknown): void {
          if (!isObjectRecord(type)) {
            throw new Error("TextEditor.setDecorations requires a decoration type");
          }
          const creation = decorationHandles.get(type);
          if (!creation) {
            throw new Error("TextEditor.setDecorations received an unknown decoration type");
          }
          if (!Array.isArray(ranges)) {
            throw new Error("TextEditor.setDecorations requires an array of ranges");
          }
          void creation
            .then((handleId) => requestHost("window.textEditor.setDecorations", [handleId, ranges, documentUri]))
            .catch((error) => {
              post("runtime-error", { error: toErrorMessage(error) });
            });
        },
      };
      return Object.freeze(editor);
    }
    const revived: Record<string, unknown> = Object.create(null);
    for (const [key, entry] of Object.entries(value)) {
      revived[key] = reviveWorkerValue(entry);
    }
    return revived;
  };

  const createRemoteDisposable = (
    registration: Promise<unknown>,
    callbackIds: number[] = [],
  ): { dispose(): void } => {
    let disposed = false;
    let disposableId: number | undefined;
    const pending = registration.then((value) => {
      if (typeof value !== "number") {
        throw new Error("Host returned an invalid disposable id");
      }
      disposableId = value;
      if (disposed) return requestHost("disposables.dispose", [value]);
      return undefined;
    });
    if (activationInProgress) registrations.push(pending);
    return Object.freeze({
      dispose(): void {
        if (disposed) return;
        disposed = true;
        for (const callbackId of callbackIds) callbacks.delete(callbackId);
        if (disposableId !== undefined) {
          void requestHost("disposables.dispose", [disposableId]).catch(
            () => undefined,
          );
        }
      },
    });
  };

  const registerRemoteProvider = (
    method: string,
    args: unknown[],
    provider: unknown,
    methodNames: readonly string[],
  ): { dispose(): void } => {
    if (!acceptingRegistrations) {
      throw new Error("Provider registration is closed after extension activation");
    }
    if (!isObjectRecord(provider)) {
      throw new Error(method + " requires a provider object");
    }
    const callbackMap: Record<string, number> = Object.create(null);
    const callbackIds: number[] = [];
    for (const methodName of methodNames) {
      const callback = provider[methodName];
      if (typeof callback !== "function") continue;
      const callbackId = nextCallbackId++;
      callbacks.set(callbackId, (...callbackArgs: unknown[]) =>
        callback.apply(provider, callbackArgs.map(reviveWorkerValue)));
      callbackMap[methodName] = callbackId;
      callbackIds.push(callbackId);
    }
    if (callbackIds.length === 0) {
      throw new Error(method + " provider exposes no supported callbacks");
    }
    return createRemoteDisposable(
      requestHost(method, [...args, callbackMap]),
      callbackIds,
    );
  };

  const createRemoteHandle = (method: string, args: unknown[]): Promise<number> => {
    const creation = requestHost(method, args).then((value) => {
      if (typeof value !== "number") {
        throw new Error("Host returned an invalid resource handle");
      }
      return value;
    });
    if (activationInProgress) registrations.push(creation);
    else void creation.catch(() => undefined);
    return creation;
  };

  const providerMethods: Record<string, readonly string[]> = {
    completion: ["provideCompletionItems"],
    hover: ["provideHover"],
    definition: ["provideDefinition"],
    codeAction: ["provideCodeActions"],
    reference: ["provideReferences"],
    codeLens: ["provideCodeLenses"],
    documentFormatting: ["provideDocumentFormattingEdits"],
    documentRangeFormatting: ["provideDocumentRangeFormattingEdits"],
    onTypeFormatting: ["provideOnTypeFormattingEdits"],
    signatureHelp: ["provideSignatureHelp"],
    workspaceSymbol: ["provideWorkspaceSymbols"],
    documentLink: ["provideDocumentLinks", "resolveDocumentLink"],
    color: ["provideDocumentColors", "provideColorPresentations"],
    foldingRange: ["provideFoldingRanges"],
    declaration: ["provideDeclaration"],
    implementation: ["provideImplementation"],
    typeDefinition: ["provideTypeDefinition"],
    rename: ["provideRenameEdits", "prepareRename"],
    documentSymbol: ["provideDocumentSymbols"],
    documentSemanticTokens: [
      "provideDocumentSemanticTokens",
      "provideDocumentSemanticTokensEdits",
    ],
    documentRangeSemanticTokens: ["provideDocumentRangeSemanticTokens"],
    documentHighlight: ["provideDocumentHighlights"],
    inlayHints: ["provideInlayHints"],
  };

  const registerLanguageProvider = (
    kind: string,
    selector: unknown,
    provider: unknown,
    extra?: unknown,
  ): { dispose(): void } => {
    const methodNames = providerMethods[kind];
    if (!methodNames) throw new Error("Unsupported language provider kind: " + kind);
    return registerRemoteProvider(
      "languages.registerProvider",
      [kind, selector, extra],
      provider,
      methodNames,
    );
  };

  class SemanticTokensLegend {
    readonly tokenTypes: string[];
    readonly tokenModifiers: string[];

    constructor(tokenTypes: readonly string[], tokenModifiers: readonly string[] = []) {
      this.tokenTypes = Array.from(tokenTypes);
      this.tokenModifiers = Array.from(tokenModifiers);
    }
  }

  class ThemeColor {
    readonly id: string;

    constructor(id: string) {
      if (typeof id !== "string" || id.trim().length === 0) {
        throw new Error("ThemeColor requires a non-empty id");
      }
      this.id = id;
    }
  }

  class Position {
    readonly line: number;
    readonly character: number;

    constructor(line: number, character: number) {
      if (!Number.isInteger(line) || line < 0 || !Number.isInteger(character) || character < 0) {
        throw new Error("Position requires non-negative integer coordinates");
      }
      this.line = line;
      this.character = character;
    }
  }

  class Range {
    readonly start: Position;
    readonly end: Position;

    constructor(
      startOrLine: Position | number,
      endOrStartCharacter: Position | number,
      endLine?: number,
      endCharacter?: number,
    ) {
      if (typeof startOrLine === "number") {
        if (typeof endOrStartCharacter !== "number" || endLine === undefined || endCharacter === undefined) {
          throw new Error("Range requires four numeric coordinates");
        }
        this.start = new Position(startOrLine, endOrStartCharacter);
        this.end = new Position(endLine, endCharacter);
      } else {
        if (typeof endOrStartCharacter === "number") {
          throw new Error("Range requires two Position values");
        }
        this.start = startOrLine;
        this.end = endOrStartCharacter;
      }
    }

    get isEmpty(): boolean {
      return this.start.line === this.end.line && this.start.character === this.end.character;
    }
  }

  class Selection extends Range {
    readonly anchor: Position;
    readonly active: Position;

    constructor(
      anchorOrLine: Position | number,
      activeOrAnchorCharacter: Position | number,
      activeLine?: number,
      activeCharacter?: number,
    ) {
      const anchor = typeof anchorOrLine === "number"
        ? new Position(anchorOrLine, activeOrAnchorCharacter as number)
        : anchorOrLine;
      const active = typeof anchorOrLine === "number"
        ? new Position(activeLine as number, activeCharacter as number)
        : activeOrAnchorCharacter as Position;
      const anchorFirst = anchor.line < active.line
        || (anchor.line === active.line && anchor.character <= active.character);
      super(anchorFirst ? anchor : active, anchorFirst ? active : anchor);
      this.anchor = anchor;
      this.active = active;
    }

    get isReversed(): boolean {
      return this.start !== this.anchor;
    }
  }

  class Uri {
    readonly scheme: string;
    readonly authority: string;
    readonly path: string;
    readonly query: string;
    readonly fragment: string;
    readonly fsPath: string;

    constructor(
      scheme: string,
      authority: string,
      path: string,
      query = "",
      fragment = "",
    ) {
      this.scheme = scheme;
      this.authority = authority;
      this.path = path;
      this.query = query;
      this.fragment = fragment;
      const decodedPath = decodeURIComponent(path);
      this.fsPath = scheme === "file" && /^\/[A-Za-z]:/.test(decodedPath)
        ? decodedPath.slice(1).replaceAll("/", "\\")
        : decodedPath;
    }

    static file(fsPath: string): Uri {
      const normalized = fsPath.replaceAll("\\", "/");
      const path = normalized.startsWith("/") ? normalized : "/" + normalized;
      return new Uri("file", "", path);
    }

    static parse(value: string): Uri {
      const match = /^([A-Za-z][A-Za-z0-9+.-]*):(?:\/\/([^/]*))?([^?#]*)(?:\?([^#]*))?(?:#(.*))?$/.exec(value);
      if (!match) throw new Error("Invalid URI: " + value);
      return new Uri(match[1], match[2] ?? "", match[3] || "/", match[4] ?? "", match[5] ?? "");
    }

    static joinPath(base: Uri, ...paths: string[]): Uri {
      let joined = base.path.replace(/\/+$/, "");
      for (const segment of paths) joined += "/" + segment.replace(/^\/+/, "");
      return new Uri(base.scheme, base.authority, joined, base.query, base.fragment);
    }

    with(change: { scheme?: string; authority?: string; path?: string; query?: string; fragment?: string }): Uri {
      return new Uri(
        change.scheme ?? this.scheme,
        change.authority ?? this.authority,
        change.path ?? this.path,
        change.query ?? this.query,
        change.fragment ?? this.fragment,
      );
    }

    toString(): string {
      const authority = this.authority ? "//" + this.authority : this.scheme === "file" ? "//" : "";
      const query = this.query ? "?" + this.query : "";
      const fragment = this.fragment ? "#" + this.fragment : "";
      return this.scheme + ":" + authority + this.path + query + fragment;
    }
  }

  const createLanguagesAPI = (): object => {
    const languages = {
      registerCompletionItemProvider: (selector: unknown, provider: unknown) =>
        registerLanguageProvider("completion", selector, provider),
      registerHoverProvider: (selector: unknown, provider: unknown) =>
        registerLanguageProvider("hover", selector, provider),
      registerDefinitionProvider: (selector: unknown, provider: unknown) =>
        registerLanguageProvider("definition", selector, provider),
      registerCodeActionProvider: (selector: unknown, provider: unknown) =>
        registerLanguageProvider("codeAction", selector, provider),
      registerReferenceProvider: (selector: unknown, provider: unknown) =>
        registerLanguageProvider("reference", selector, provider),
      registerCodeLensProvider: (selector: unknown, provider: unknown) =>
        registerLanguageProvider("codeLens", selector, provider),
      registerDocumentFormattingEditProvider: (selector: unknown, provider: unknown) =>
        registerLanguageProvider("documentFormatting", selector, provider),
      registerDocumentRangeFormattingEditProvider: (selector: unknown, provider: unknown) =>
        registerLanguageProvider("documentRangeFormatting", selector, provider),
      registerOnTypeFormattingEditProvider: (
        selector: unknown,
        provider: unknown,
        firstTriggerCharacter: string,
        moreTriggerCharacter?: string[],
      ) => registerLanguageProvider("onTypeFormatting", selector, provider, {
        firstTriggerCharacter,
        moreTriggerCharacter,
      }),
      registerSignatureHelpProvider: (selector: unknown, provider: unknown) =>
        registerLanguageProvider("signatureHelp", selector, provider),
      registerWorkspaceSymbolProvider: (provider: unknown) =>
        registerLanguageProvider("workspaceSymbol", { language: "*" }, provider),
      registerDocumentLinkProvider: (selector: unknown, provider: unknown) =>
        registerLanguageProvider("documentLink", selector, provider),
      registerColorProvider: (selector: unknown, provider: unknown) =>
        registerLanguageProvider("color", selector, provider),
      registerFoldingRangeProvider: (selector: unknown, provider: unknown) =>
        registerLanguageProvider("foldingRange", selector, provider),
      registerDeclarationProvider: (selector: unknown, provider: unknown) =>
        registerLanguageProvider("declaration", selector, provider),
      registerImplementationProvider: (selector: unknown, provider: unknown) =>
        registerLanguageProvider("implementation", selector, provider),
      registerTypeDefinitionProvider: (selector: unknown, provider: unknown) =>
        registerLanguageProvider("typeDefinition", selector, provider),
      registerRenameProvider: (selector: unknown, provider: unknown) =>
        registerLanguageProvider("rename", selector, provider),
      registerDocumentSymbolProvider: (selector: unknown, provider: unknown) =>
        registerLanguageProvider("documentSymbol", selector, provider),
      registerDocumentSemanticTokensProvider: (selector: unknown, provider: unknown, legend?: unknown) =>
        registerLanguageProvider("documentSemanticTokens", selector, provider, legend),
      registerDocumentRangeSemanticTokensProvider: (selector: unknown, provider: unknown) =>
        registerLanguageProvider("documentRangeSemanticTokens", selector, provider),
      registerDocumentHighlightProvider: (selector: unknown, provider: unknown) =>
        registerLanguageProvider("documentHighlight", selector, provider),
      registerInlayHintsProvider: (selector: unknown, provider: unknown) =>
        registerLanguageProvider("inlayHints", selector, provider),
    };
    return new Proxy(Object.freeze(languages), {
      get(target, property, receiver) {
        if (typeof property === "symbol" || property in target) {
          return Reflect.get(target, property, receiver);
        }
        throw unsupportedApi("vscode.languages." + String(property));
      },
    });
  };

  const createWorkspaceAPI = (): object => {
    const registerDocumentEvent = (
      method: string,
      listener: unknown,
    ): { dispose(): void } =>
      registerRemoteProvider(method, [], { listener }, ["listener"]);
    const workspace = {
      fs: new Proxy(Object.freeze({
        readFile: (uri: unknown) => requestHost("workspace.fs.readFile", [uri]),
        writeFile: (uri: unknown, content: unknown) =>
          requestHost("workspace.fs.writeFile", [uri, content]),
        exists: (uri: unknown) => requestHost("workspace.fs.exists", [uri]),
        createDirectory: (uri: unknown) =>
          requestHost("workspace.fs.createDirectory", [uri]),
        rename: (oldUri: unknown, newUri: unknown, options?: unknown) =>
          requestHost("workspace.fs.rename", [oldUri, newUri, options]),
        delete: (uri: unknown, options?: unknown) =>
          requestHost("workspace.fs.delete", [uri, options]),
        readDirectory: (uri: unknown) =>
          requestHost("workspace.fs.readDirectory", [uri]),
      }), {
        get(target, property, receiver) {
          if (typeof property === "symbol" || property in target) {
            return Reflect.get(target, property, receiver);
          }
          throw unsupportedApi("vscode.workspace.fs." + String(property));
        },
      }),
      get workspaceFolders(): unknown {
        return workspaceFoldersSnapshot === undefined
          ? undefined
          : reviveWorkerValue(workspaceFoldersSnapshot);
      },
      getConfiguration: (section?: string) => {
        const cacheKey = section ?? "";
        void requestHost("workspace.getConfiguration", [section]).then((snapshot) => {
          if (isObjectRecord(snapshot)) configurationSnapshots.set(cacheKey, snapshot);
        }).catch(() => undefined);
        const rootConfig = configurationSnapshots.get("");
        const cached = configurationSnapshots.get(cacheKey);
        const configData = cached
          ?? (cacheKey && rootConfig && isObjectRecord(rootConfig[cacheKey])
            ? rootConfig[cacheKey] as Record<string, unknown>
            : rootConfig)
          ?? Object.create(null) as Record<string, unknown>;
        return Object.freeze({
          get: <T>(key: string, defaultValue?: T): T => {
            let current: unknown = configData;
            for (const part of key.split(".")) {
              if (!isObjectRecord(current) || !(part in current)) return defaultValue as T;
              current = current[part];
            }
            return (current === undefined ? defaultValue : current) as T;
          },
          has: (key: string): boolean => {
            let current: unknown = configData;
            for (const part of key.split(".")) {
              if (!isObjectRecord(current) || !(part in current)) return false;
              current = current[part];
            }
            return true;
          },
        });
      },
      onDidChangeConfiguration: (listener: unknown) =>
        registerDocumentEvent("workspace.onDidChangeConfiguration", listener),
      createFileSystemWatcher: (globPattern: unknown) => {
        const creation = createRemoteHandle("workspace.createFileSystemWatcher", [globPattern]);
        const listenerDisposables = new Set<{ dispose(): void }>();
        const register = (event: string, listener: unknown): { dispose(): void } => {
          if (typeof listener !== "function") throw new Error(event + " requires a listener");
          const callbackId = nextCallbackId++;
          callbacks.set(callbackId, (...callbackArgs: unknown[]) =>
            (listener as (...args: unknown[]) => unknown)(...callbackArgs.map(reviveWorkerValue)),
          );
          const remote = createRemoteDisposable(
            creation.then((handleId) => requestHost(event, [handleId, { listener: callbackId }])),
            [callbackId],
          );
          const disposable = {
            dispose(): void {
              remote.dispose();
              listenerDisposables.delete(disposable);
            },
          };
          listenerDisposables.add(disposable);
          return disposable;
        };
        return {
          onDidCreate: (listener: unknown) => register("workspace.watcher.onDidCreate", listener),
          onDidChange: (listener: unknown) => register("workspace.watcher.onDidChange", listener),
          onDidDelete: (listener: unknown) => register("workspace.watcher.onDidDelete", listener),
          dispose(): void {
            for (const listener of listenerDisposables) listener.dispose();
            listenerDisposables.clear();
            void creation.then((handleId) => requestHost("workspace.watcher.dispose", [handleId])).catch(() => undefined);
          },
        };
      },
      findFiles: (include: unknown, exclude?: unknown, maxResults?: number) =>
        requestHost("workspace.findFiles", [include, exclude, maxResults]),
      findTextInFiles: (query: unknown, options: unknown) =>
        requestHost("workspace.findTextInFiles", [query, options]),
      openTextDocument: (uri: unknown) =>
        requestHost("workspace.openTextDocument", [uri]).then(reviveWorkerValue),
      saveAll: (includeUntitled?: boolean) =>
        requestHost("workspace.saveAll", [includeUntitled]),
      onDidSaveTextDocument: (listener: unknown) =>
        registerDocumentEvent("workspace.onDidSaveTextDocument", listener),
      onDidChangeTextDocument: (listener: unknown) =>
        registerDocumentEvent("workspace.onDidChangeTextDocument", listener),
      onDidOpenTextDocument: (listener: unknown) =>
        registerDocumentEvent("workspace.onDidOpenTextDocument", listener),
      onDidCloseTextDocument: (listener: unknown) =>
        registerDocumentEvent("workspace.onDidCloseTextDocument", listener),
    };
    return new Proxy(Object.freeze(workspace), {
      get(target, property, receiver) {
        if (typeof property === "symbol" || property in target) {
          return Reflect.get(target, property, receiver);
        }
        throw unsupportedApi("vscode.workspace." + String(property));
      },
    });
  };

  const createCommandsAPI = (): object => {
    const commands = {
      registerCommand(
        command: string,
        callback: (...args: unknown[]) => unknown,
      ): { dispose(): void } {
        if (!acceptingRegistrations) {
          throw new Error("Command registration is closed after extension activation");
        }
        if (typeof command !== "string" || command.trim().length === 0) {
          throw new Error("commands.registerCommand requires a non-empty command id");
        }
        if (typeof callback !== "function") {
          throw new Error("commands.registerCommand requires a callback");
        }

        const callbackId = nextCallbackId++;
        callbacks.set(callbackId, callback);
        let disposed = false;
        let disposableId: number | undefined;
        const registration = requestHost("commands.registerCommand", [
          command,
          callbackId,
        ]).then((value) => {
          if (typeof value !== "number") {
            throw new Error("Host returned an invalid command disposable id");
          }
          disposableId = value;
          if (disposed) {
            return requestHost("commands.dispose", [value]);
          }
          return undefined;
        });
        if (activationInProgress) registrations.push(registration);

        return Object.freeze({
          dispose(): void {
            if (disposed) return;
            disposed = true;
            callbacks.delete(callbackId);
            if (disposableId !== undefined) {
              void requestHost("commands.dispose", [disposableId]).catch(
                () => undefined,
              );
            }
          },
        });
      },
      executeCommand(command: string, ...args: unknown[]): Promise<unknown> {
        if (typeof command !== "string" || command.trim().length === 0) {
          return Promise.reject(
            new Error("commands.executeCommand requires a non-empty command id"),
          );
        }
        return requestHost("commands.executeCommand", [command, ...args]);
      },
    };

    return new Proxy(Object.freeze(commands), {
      get(target, property, receiver) {
        if (typeof property === "symbol" || property in target) {
          return Reflect.get(target, property, receiver);
        }
        throw unsupportedApi("vscode.commands." + String(property));
      },
    });
  };

  const createSecretsAPI = (): object => {
    const secrets = {
      get(key: string): Promise<unknown> {
        if (typeof key !== "string" || key.length === 0) {
          return Promise.reject(
            new Error("secrets.get requires a non-empty key"),
          );
        }
        return requestHost("secrets.get", [key]);
      },
      store(key: string, value: string): Promise<unknown> {
        if (typeof key !== "string" || key.length === 0) {
          return Promise.reject(
            new Error("secrets.store requires a non-empty key"),
          );
        }
        if (typeof value !== "string") {
          return Promise.reject(
            new Error("secrets.store requires a string value"),
          );
        }
        return requestHost("secrets.store", [key, value]);
      },
      delete(key: string): Promise<unknown> {
        if (typeof key !== "string" || key.length === 0) {
          return Promise.reject(
            new Error("secrets.delete requires a non-empty key"),
          );
        }
        return requestHost("secrets.delete", [key]);
      },
    };

    return new Proxy(Object.freeze(secrets), {
      get(target, property, receiver) {
        if (typeof property === "symbol" || property in target) {
          return Reflect.get(target, property, receiver);
        }
        throw unsupportedApi("vscode.secrets." + String(property));
      },
    });
  };

  const createTasksAPI = (): object => {
    const tasks = {
      registerTaskProvider(type: string, provider: unknown): { dispose(): void } {
        if (typeof type !== "string" || type.trim().length === 0) {
          throw new Error("tasks.registerTaskProvider requires a non-empty type");
        }
        return registerRemoteProvider(
          "tasks.registerTaskProvider",
          [type],
          provider,
          ["provideTasks", "resolveTask"],
        );
      },
      async executeTask(task: object): Promise<object> {
        const handleId = await requestHost("tasks.executeTask", [task]);
        if (typeof handleId !== "number") {
          throw new Error("Host returned an invalid task execution handle");
        }
        let terminated = false;
        let completed = false;
        taskCompletionHandlers.set(handleId, () => {
          completed = true;
          taskCompletionHandlers.delete(handleId);
        });
        return Object.freeze({
          task,
          terminate(): void {
            if (terminated || completed) return;
            terminated = true;
            void requestHost("tasks.terminate", [handleId]).catch(
              () => undefined,
            );
          },
        });
      },
      fetchTasks(): Promise<unknown> {
        return requestHost("tasks.fetchTasks", []);
      },
    };

    return new Proxy(Object.freeze(tasks), {
      get(target, property, receiver) {
        if (typeof property === "symbol" || property in target) {
          return Reflect.get(target, property, receiver);
        }
        throw unsupportedApi("vscode.tasks." + String(property));
      },
    });
  };

  const createWindowAPI = (): object => {
    const registerWindowEvent = (
      method: string,
      listener: unknown,
    ): { dispose(): void } =>
      registerRemoteProvider(method, [], { listener }, ["listener"]);

    const createOutputChannel = (name: string): object => {
      if (typeof name !== "string" || name.trim().length === 0) {
        throw new Error("window.createOutputChannel requires a non-empty name");
      }
      const creation = createRemoteHandle("window.createOutputChannel", [name]);
      let disposed = false;
      const invoke = (method: string, args: unknown[] = []): void => {
        if (disposed && method !== "window.output.dispose") return;
        void creation
          .then((handleId) => requestHost(method, [handleId, ...args]))
          .catch(() => undefined);
      };
      return Object.freeze({
        name,
        append(value: string): void {
          invoke("window.output.append", [value]);
        },
        appendLine(value: string): void {
          invoke("window.output.appendLine", [value]);
        },
        clear(): void {
          invoke("window.output.clear");
        },
        show(preserveFocus = false): void {
          invoke("window.output.show", [preserveFocus]);
        },
        hide(): void {
          invoke("window.output.hide");
        },
        dispose(): void {
          if (disposed) return;
          disposed = true;
          invoke("window.output.dispose");
        },
      });
    };

    const createWebviewPanel = (
      viewType: string,
      title: string,
      showOptions: unknown,
      options?: unknown,
    ): object => {
      const creation = createRemoteHandle("window.createWebviewPanel", [
        viewType,
        title,
        showOptions,
        options,
      ]);
      let html = "";
      let disposed = false;
      const webview = Object.freeze({
        get html(): string {
          return html;
        },
        set html(value: string) {
          if (disposed) return;
          html = String(value);
          void creation
            .then((handleId) =>
              requestHost("window.webview.setHtml", [handleId, html]))
            .catch(() => undefined);
        },
        get _iframe(): undefined {
          return undefined;
        },
      });
      return Object.freeze({
        viewType,
        title,
        webview,
        visible: true,
        active: true,
        dispose(): void {
          if (disposed) return;
          disposed = true;
          void creation
            .then((handleId) => requestHost("window.webview.dispose", [handleId]))
            .catch(() => undefined);
        },
        onDidDispose(listener: unknown): { dispose(): void } {
          if (typeof listener !== "function") {
            throw new Error("WebviewPanel.onDidDispose requires a listener");
          }
          const callbackId = nextCallbackId++;
          callbacks.set(callbackId, listener as (...args: unknown[]) => unknown);
          return createRemoteDisposable(
            creation.then((handleId) =>
              requestHost("window.webview.onDidDispose", [handleId, callbackId])),
            [callbackId],
          );
        },
      });
    };

    const windowApi = {
      createWebviewPanel,
      showInformationMessage(message: string, ...items: string[]): Promise<unknown> {
        return requestHost("window.showMessage", ["info", message, items]);
      },
      showWarningMessage(message: string, ...items: string[]): Promise<unknown> {
        return requestHost("window.showMessage", ["warn", message, items]);
      },
      showErrorMessage(message: string, ...items: string[]): Promise<unknown> {
        return requestHost("window.showMessage", ["error", message, items]);
      },
      createTextEditorDecorationType(options: unknown): object {
        if (!isObjectRecord(options)) {
          throw new Error("window.createTextEditorDecorationType requires render options");
        }
        const key = "worker-decoration-" + nextDecorationTypeKey++;
        let disposed = false;
        const creation = createRemoteHandle("window.createTextEditorDecorationType", [options]);
        const type: Record<string, unknown> = {
          key,
          dispose(): void {
            if (disposed) return;
            disposed = true;
            void creation
              .then((handleId) => requestHost("disposables.dispose", [handleId]))
              .catch(() => undefined);
          },
        };
        decorationHandles.set(type, creation);
        return Object.freeze(type);
      },
      showInputBox(options?: unknown): Promise<unknown> {
        if (options !== undefined && !isObjectRecord(options)) {
          return Promise.reject(new Error("window.showInputBox requires an options object"));
        }
        const serialized = options ? { ...options } : undefined;
        let validateCallbackId: number | undefined;
        if (serialized && typeof serialized.validateInput === "function") {
          validateCallbackId = nextCallbackId++;
          const validator = serialized.validateInput as (value: string) => unknown;
          callbacks.set(validateCallbackId, (...args: unknown[]) =>
            validator(String(args[0] ?? "")),
          );
          delete serialized.validateInput;
        }
        return requestHost("window.showInputBox", [serialized, validateCallbackId])
          .finally(() => {
            if (validateCallbackId !== undefined) callbacks.delete(validateCallbackId);
          });
      },
      showQuickPick(items: unknown[], options?: unknown): Promise<unknown> {
        return requestHost("window.showQuickPick", [items, options]);
      },
      setStatusBarMessage(text: string, hideAfter?: number): { dispose(): void } {
        const creation = createRemoteHandle("window.setStatusBarMessage", [text, hideAfter]);
        return Object.freeze({
          dispose(): void {
            void creation.then((handleId) => requestHost("window.status.dispose", [handleId])).catch(() => undefined);
          },
        });
      },
      createStatusBarItem(): object {
        const creation = createRemoteHandle("window.createStatusBarItem", []);
        let disposed = false;
        const invoke = (method: string, args: unknown[] = []): void => {
          if (disposed && method !== "window.status.dispose") return;
          void creation.then((handleId) => requestHost(method, [handleId, ...args])).catch(() => undefined);
        };
        let text = "";
        let tooltip: string | undefined;
        let command: string | undefined;
        const item = {
          get text(): string { return text; },
          set text(value: string) { text = value; invoke("window.status.setText", [value]); },
          get tooltip(): string | undefined { return tooltip; },
          set tooltip(value: string | undefined) { tooltip = value; invoke("window.status.setTooltip", [value]); },
          get command(): string | undefined { return command; },
          set command(value: string | undefined) { command = value; invoke("window.status.setCommand", [value]); },
          show(): void { invoke("window.status.show"); },
          hide(): void { invoke("window.status.hide"); },
          dispose(): void { if (!disposed) { disposed = true; invoke("window.status.dispose"); } },
        };
        return item;
      },
      withProgress(options: unknown, task: (progress: unknown) => unknown): Promise<unknown> {
        if (typeof task !== "function") throw new Error("window.withProgress requires a callback");
        const callbackId = nextCallbackId++;
        callbacks.set(callbackId, async (...args: unknown[]) => {
          const progressId = callbackId;
          pendingProgressReports.set(progressId, new Set<Promise<unknown>>());
          try {
            const result = await task(args[0]);
            const reports = pendingProgressReports.get(progressId);
            if (reports && reports.size > 0) await Promise.all([...reports]);
            return result;
          } finally {
            pendingProgressReports.delete(progressId);
          }
        });
        const result = requestHost("window.withProgress", [options, callbackId])
          .finally(() => {
            callbacks.delete(callbackId);
            pendingProgressReports.delete(callbackId);
          });
        return result;
      },
      createOutputChannel,
      createTerminal(options?: Record<string, unknown>): object {
        let disposed = false;
        const creation = requestHost("window.createTerminal", [options]).then(
          (handleId) => {
            if (typeof handleId !== "number") {
              throw new Error("Host returned an invalid terminal handle");
            }
            return handleId;
          },
        );
        if (activationInProgress) {
          registrations.push(creation);
        } else {
          void creation.catch(() => undefined);
        }

        const invoke = (method: string, args: unknown[] = []): void => {
          if (disposed && method !== "window.terminal.dispose") return;
          void creation
            .then((handleId) => requestHost(method, [handleId, ...args]))
            .catch(() => undefined);
        };
        return Object.freeze({
          name:
            typeof options?.name === "string"
              ? options.name
              : "Extension Terminal",
          sendText(text: string, addNewLine = true): void {
            if (typeof text !== "string") {
              throw new Error("Terminal.sendText requires string input");
            }
            invoke("window.terminal.sendText", [text, addNewLine]);
          },
          show(preserveFocus = false): void {
            invoke("window.terminal.show", [preserveFocus]);
          },
          hide(): void {
            invoke("window.terminal.hide");
          },
          dispose(): void {
            if (disposed) return;
            disposed = true;
            invoke("window.terminal.dispose");
          },
        });
      },
      registerTreeDataProvider(viewId: string, provider: unknown): { dispose(): void } {
        return registerRemoteProvider(
          "window.registerTreeDataProvider",
          [viewId],
          provider,
          ["getTreeItem", "getChildren", "getParent"],
        );
      },
      registerWebviewViewProvider(viewId: string, provider: unknown): { dispose(): void } {
        return registerRemoteProvider(
          "window.registerWebviewViewProvider",
          [viewId],
          provider,
          ["resolveWebviewView"],
        );
      },
      onDidChangeActiveTextEditor: (listener: unknown) =>
        registerWindowEvent("window.onDidChangeActiveTextEditor", listener),
      onDidChangeTextEditorSelection: (listener: unknown) =>
        registerWindowEvent("window.onDidChangeTextEditorSelection", listener),
      get activeTextEditor(): unknown {
        return activeTextEditorSnapshot === undefined
          ? undefined
          : reviveWorkerValue(activeTextEditorSnapshot);
      },
    };

    return new Proxy(Object.freeze(windowApi), {
      get(target, property, receiver) {
        if (typeof property === "symbol" || property in target) {
          return Reflect.get(target, property, receiver);
        }
        throw unsupportedApi("vscode.window." + String(property));
      },
    });
  };

  const createDebugAPI = (): object => new Proxy(Object.freeze({
    registerDebugConfigurationProvider(type: string, provider: unknown) {
      return registerRemoteProvider(
        "debug.registerConfigurationProvider",
        [type],
        provider,
        ["provideDebugConfigurations", "resolveDebugConfiguration"],
      );
    },
    startDebugging(folder: unknown, config: unknown): Promise<unknown> {
      return requestHost("debug.startDebugging", [folder, config]);
    },
  }), {
    get(target, property, receiver) {
      if (typeof property === "symbol" || property in target) {
        return Reflect.get(target, property, receiver);
      }
      throw unsupportedApi("vscode.debug." + String(property));
    },
  });

  const createSourceControl = (
    id: string,
    label: string,
    rootUri?: unknown,
  ): object => {
    const creation = createRemoteHandle("scm.createSourceControl", [id, label, rootUri]);
    let disposed = false;
    const inputBox: Record<string, unknown> = {};
    let inputValue = "";
    let inputPlaceholder: string | undefined;
    Object.defineProperties(inputBox, {
      value: {
        enumerable: true,
        get: () => inputValue,
        set: (value: unknown) => {
          inputValue = String(value ?? "");
          void creation
            .then((handleId) =>
              requestHost("scm.setInputBox", [handleId, inputValue, inputPlaceholder]))
            .catch(() => undefined);
        },
      },
      placeholder: {
        enumerable: true,
        get: () => inputPlaceholder,
        set: (value: unknown) => {
          inputPlaceholder = value === undefined ? undefined : String(value);
          void creation
            .then((handleId) =>
              requestHost("scm.setInputBox", [handleId, inputValue, inputPlaceholder]))
            .catch(() => undefined);
        },
      },
    });

    return Object.freeze({
      id,
      label,
      rootUri,
      inputBox,
      createResourceGroup(groupId: string, groupLabel: string): object {
        const groupCreation = creation.then((handleId) =>
          createRemoteHandle("scm.createResourceGroup", [handleId, groupId, groupLabel]));
        let resourceStates: unknown[] = [];
        let groupDisposed = false;
        return Object.freeze({
          id: groupId,
          label: groupLabel,
          get resourceStates(): unknown[] {
            return resourceStates;
          },
          set resourceStates(value: unknown[]) {
            resourceStates = Array.isArray(value) ? value : [];
            void groupCreation
              .then((handleId) =>
                requestHost("scm.setResourceStates", [handleId, resourceStates]))
              .catch(() => undefined);
          },
          dispose(): void {
            if (groupDisposed) return;
            groupDisposed = true;
            void groupCreation
              .then((handleId) => requestHost("scm.disposeResourceGroup", [handleId]))
              .catch(() => undefined);
          },
        });
      },
      dispose(): void {
        if (disposed) return;
        disposed = true;
        void creation
          .then((handleId) => requestHost("scm.dispose", [handleId]))
          .catch(() => undefined);
      },
    });
  };

  const createScmAPI = (): object => new Proxy(Object.freeze({ createSourceControl }), {
    get(target, property, receiver) {
      if (typeof property === "symbol" || property in target) {
        return Reflect.get(target, property, receiver);
      }
      throw unsupportedApi("vscode.scm." + String(property));
    },
  });

  const createEnvAPI = (): object => new Proxy(Object.freeze({
    clipboard: new Proxy(Object.freeze({
      readText: () => requestHost("env.clipboard.readText", []),
      writeText: (value: string) => requestHost("env.clipboard.writeText", [value]),
    }), {
      get(target, property, receiver) {
        if (typeof property === "symbol" || property in target) {
          return Reflect.get(target, property, receiver);
        }
        throw unsupportedApi("vscode.env.clipboard." + String(property));
      },
    }),
    openExternal: (uri: unknown) => requestHost("env.openExternal", [uri]),
    get machineId(): string {
      return hostMachineId;
    },
    get sessionId(): string {
      return hostSessionId;
    },
  }), {
    get(target, property, receiver) {
      if (typeof property === "symbol" || property in target) {
        return Reflect.get(target, property, receiver);
      }
      throw unsupportedApi("vscode.env." + String(property));
    },
  });

  const createVscodeProxy = (): object => {
    const api = Object.freeze({
      languages: createLanguagesAPI(),
      SemanticTokensLegend,
      ThemeColor,
      Selection,
      StatusBarAlignment: Object.freeze({ Left: 1, Right: 2 }),
      TextEditorRevealType: Object.freeze({
        Default: 0,
        InCenter: 1,
        InCenterIfOutsideViewport: 2,
        AtTop: 3,
      }),
      Position,
      Range,
      Uri,
      commands: createCommandsAPI(),
      workspace: createWorkspaceAPI(),
      secrets: createSecretsAPI(),
      tasks: createTasksAPI(),
      window: createWindowAPI(),
      debug: createDebugAPI(),
      scm: createScmAPI(),
      env: createEnvAPI(),
    });
    return new Proxy(api, {
      get(target, property, receiver) {
        if (typeof property === "symbol" || property in target) {
          return Reflect.get(target, property, receiver);
        }
        throw unsupportedApi("vscode." + String(property));
      },
    });
  };

  const disableGlobal = (name: string): void => {
    let current: object | null = scope;
    let found = false;
    while (current) {
      const descriptor = Object.getOwnPropertyDescriptor(current, name);
      if (descriptor) {
        found = true;
        if (!descriptor.configurable && current !== scope) {
          throw new Error(`Unable to disable inherited Worker capability: ${name}`);
        }
        if ("value" in descriptor) {
          Object.defineProperty(current, name, {
            configurable: descriptor.configurable,
            enumerable: descriptor.enumerable,
            value: undefined,
            writable: descriptor.writable,
          });
        } else {
          Object.defineProperty(current, name, {
            configurable: descriptor.configurable,
            enumerable: descriptor.enumerable,
            get: undefined,
            set: undefined,
          });
        }
      }
      current = Object.getPrototypeOf(current) as object | null;
    }
    if (!found) {
      Object.defineProperty(scope, name, {
        configurable: false,
        enumerable: false,
        value: undefined,
        writable: false,
      });
    }
  };

  const loadCommonJSModule = async (
    vscodeApi: object,
  ): Promise<LoadedExtensionModule> => {
    const loader = scope.__koyoriIdeLoadCommonJSModule;
    delete scope.__koyoriIdeLoadCommonJSModule;
    if (typeof loader !== "function") {
      throw new Error("Extension Worker CommonJS loader is unavailable");
    }
    // Keep the callable Function constructor unreachable, but preserve the
    // standard prototype helpers that bundled libraries use for binding.
    // Dynamic source construction still throws through the facade below.
    const nativeFunction = Function;
    for (const name of [
      "BroadcastChannel",
      "EventSource",
      "SharedWorker",
      "WebSocket",
      "WebTransport",
      "Worker",
      "XMLHttpRequest",
      "caches",
      "eval",
      "fetch",
      "importScripts",
      "indexedDB",
    ]) {
      disableGlobal(name);
    }
    const blockedFunction = function koyoriBlockedFunction(): never {
      throw new Error("Dynamic code generation is disabled in extension Workers");
    };
    const blockedPrototype = Object.create(null) as Record<PropertyKey, unknown>;
    for (const property of Object.getOwnPropertyNames(nativeFunction.prototype)) {
      if (property === "constructor") continue;
      const descriptor = Object.getOwnPropertyDescriptor(nativeFunction.prototype, property);
      if (descriptor) Object.defineProperty(blockedPrototype, property, descriptor);
    }
    Object.defineProperty(blockedPrototype, "constructor", {
      configurable: false,
      enumerable: false,
      writable: false,
      value: blockedFunction,
    });
    Object.defineProperty(blockedFunction, "prototype", {
      configurable: false,
      enumerable: false,
      writable: false,
      value: blockedPrototype,
    });
    Object.defineProperty(scope, "Function", {
      configurable: false,
      enumerable: false,
      writable: false,
      value: blockedFunction,
    });
    const candidate = await loader(vscodeApi);
    if (
      !isObjectRecord(candidate) ||
      typeof candidate.activate !== "function"
    ) {
      throw new Error(
        "Extension main module does not export an activate() function",
      );
    }
    return candidate as LoadedExtensionModule;
  };

  const handleInit = async (message: Record<string, unknown>): Promise<void> => {
    if (protocolToken) return;
    const protocolVersions = message.protocolVersions;
    if (
      typeof message.token !== "string" ||
      typeof message.mainPath !== "string" ||
      !Array.isArray(protocolVersions) ||
      !protocolVersions.every((version): version is string => typeof version === "string")
    ) {
      return;
    }
    protocolToken = message.token;
    const negotiatedProtocolVersion = supportedProtocolVersions.find(
      (version) => protocolVersions.includes(version),
    );
    if (!negotiatedProtocolVersion) {
      post("protocol-error", {
        error: "No compatible Extension Worker protocol version",
        supportedProtocolVersions,
      });
      return;
    }
    post("protocol-ready", { protocolVersion: negotiatedProtocolVersion });
    hostMachineId = typeof message.machineId === "string" ? message.machineId : "";
    hostSessionId = typeof message.sessionId === "string" ? message.sessionId : "";
    if (isObjectRecord(message.configuration)) {
      configurationSnapshots.set("", message.configuration);
    }
    if (message.activeTextEditor !== undefined) {
      activeTextEditorSnapshot = message.activeTextEditor;
    }
    if (message.workspaceFolders !== undefined) {
      workspaceFoldersSnapshot = message.workspaceFolders;
    }
    try {
      const api = createVscodeProxy();
      const loadedModule = await loadCommonJSModule(api);
      extensionModule = loadedModule;
      const extensionPath = "/extensions/" + String(message.extensionId ?? "unknown");
      const extensionUri = {
        scheme: "koyori-extension",
        authority: "",
        path: extensionPath,
        fsPath: extensionPath,
        query: "",
        fragment: "",
        toString: () => "koyori-extension:" + extensionPath,
      };
      const context = Object.freeze(
        Object.assign({
          subscriptions: extensionSubscriptions,
          extensionUri,
          extensionPath,
        }, api),
      );
      await loadedModule.activate(context);
      activationInProgress = false;
      await Promise.all(registrations);
      post("activated");
    } catch (error) {
      activationInProgress = false;
      acceptingRegistrations = false;
      post("activation-error", { error: toErrorMessage(error) });
    }
  };

  const handleCallback = async (
    message: Record<string, unknown>,
  ): Promise<void> => {
    const id = message.id;
    const callbackId = message.callbackId;
    const args = Array.isArray(message.args) ? message.args : [];
    if (typeof id !== "number" || typeof callbackId !== "number") return;
    const callback = callbacks.get(callbackId);
    if (!callback) {
      post("callback-result", {
        id,
        error: "Extension command callback is no longer registered",
      });
      return;
    }
    try {
      const result = await callback(...args.map(reviveWorkerValue));
      post("callback-result", { id, result });
    } catch (error) {
      post("callback-result", { id, error: toErrorMessage(error) });
    }
  };

  const handleDeactivate = async (
    message: Record<string, unknown>,
  ): Promise<void> => {
    if (typeof message.id !== "number") return;
    activationInProgress = false;
    acceptingRegistrations = false;
    let failure: unknown;
    try {
      if (typeof extensionModule?.deactivate === "function") {
        await extensionModule.deactivate();
      }
    } catch (error) {
      failure = error;
    } finally {
      for (let index = extensionSubscriptions.length - 1; index >= 0; index--) {
        const subscription = extensionSubscriptions[index];
        try {
          if (
            isObjectRecord(subscription) &&
            typeof subscription.dispose === "function"
          ) {
            await subscription.dispose();
          }
        } catch (error) {
          failure ??= error;
        }
      }
      extensionSubscriptions.length = 0;
    }
    if (failure !== undefined) {
      post("deactivated", {
        id: message.id,
        error: toErrorMessage(failure),
      });
    } else {
      post("deactivated", { id: message.id });
    }
  };

  const handleTerminate = async (): Promise<void> => {
    activationInProgress = false;
    acceptingRegistrations = false;
    try {
      if (typeof extensionModule?.deactivate === "function") {
        await extensionModule.deactivate();
      }
    } catch {
      // Native termination below remains the final cleanup authority.
    } finally {
      for (let index = extensionSubscriptions.length - 1; index >= 0; index--) {
        try {
          await extensionSubscriptions[index].dispose();
        } catch {
          // Continue releasing the remaining subscriptions.
        }
      }
      extensionSubscriptions.length = 0;
      callbacks.clear();
      taskCompletionHandlers.clear();
      const error = new Error("Extension Worker terminated");
      for (const pending of pendingHostRequests.values()) {
        pending.reject(error);
      }
      pendingHostRequests.clear();
      scope.close();
    }
  };

  scope.addEventListener("message", (event) => {
    const message = event.data;
    if (!isObjectRecord(message) || typeof message.type !== "string") return;
    if (message.type === "init") {
      void handleInit(message).catch(() => undefined);
      return;
    }
    if (!protocolToken || message.token !== protocolToken) return;

    if (message.type === "rpc-result" && typeof message.id === "number") {
      const pending = pendingHostRequests.get(message.id);
      if (!pending) return;
      pendingHostRequests.delete(message.id);
      if (typeof message.error === "string") {
        pending.reject(new Error(message.error));
      } else {
        pending.resolve(message.result);
      }
      return;
    }
    if (message.type === "invoke-callback") {
      void handleCallback(message).catch(() => undefined);
      return;
    }
    if (message.type === "task-completed" && typeof message.handleId === "number") {
      taskCompletionHandlers.get(message.handleId)?.();
      return;
    }
    if (message.type === "health-check" && typeof message.id === "number") {
      post("health-response", { id: message.id });
      return;
    }
    if (message.type === "deactivate") {
      void handleDeactivate(message).catch(() => undefined);
      return;
    }
    if (message.type === "terminate") {
      void handleTerminate().catch(() => undefined);
    }
  });
}

function createProtocolToken(): string {
  if (typeof crypto === "undefined" || typeof crypto.getRandomValues !== "function") {
    throw new Error("Secure randomness is unavailable for extension Worker RPC");
  }
  const values = new Uint32Array(4);
  crypto.getRandomValues(values);
  return Array.from(values, (value) => value.toString(16).padStart(8, "0")).join("");
}

function createExtensionWorker(
  extensionId: string,
  mainPath: string,
  source: string,
): Worker {
  if (
    typeof Worker === "undefined" ||
    typeof Blob === "undefined" ||
    typeof URL.createObjectURL !== "function"
  ) {
    throw new Error("Dedicated extension Workers are not available");
  }
  const normalizedPath = mainPath.replace(/[\r\n]/g, "");
  const slash = normalizedPath.lastIndexOf("/");
  const dirname = slash >= 0 ? normalizedPath.slice(0, slash) : ".";
  const commonJSLoader = [
    "globalThis.__koyoriIdeLoadCommonJSModule = async (__koyoriIdeVscodeApi) => {",
    "const module = { exports: {} };",
    "const exports = module.exports;",
    "const require = (specifier) => {",
    "  if (specifier === \"vscode\") return __koyoriIdeVscodeApi;",
    "  throw new Error(\"Unsupported CommonJS module: \" + String(specifier));",
    "};",
    "const __filename = " + JSON.stringify(normalizedPath) + ";",
    "const __dirname = " + JSON.stringify(dirname) + ";",
    source,
    "return module.exports && module.exports.default",
    "  ? module.exports.default",
    "  : module.exports;",
    "};",
    "//# sourceURL=" + normalizedPath,
  ].join("\n");
  const bootstrapURL = URL.createObjectURL(
    new Blob([commonJSLoader, "\n(", extensionWorkerBootstrap.toString(), ")();"], {
      type: "text/javascript",
    }),
  );
  try {
    return new Worker(bootstrapURL, {
      name: "koyori-ide-extension-" + extensionId,
      type: "module",
    });
  } finally {
    URL.revokeObjectURL(bootstrapURL);
  }
}

export class WorkerExtensionModule implements ExtensionModule {
  private readonly extensionId: string;
  private readonly mainPath: string;
  private readonly source: string;
  private readonly onDisposed: () => void;
  private readonly onRuntimeFailure: (error: Error) => void;
  private readonly runtimePolicy: NormalizedExtensionWorkerRuntimePolicy;
  private readonly token = createProtocolToken();
  private status: WorkerModuleStatus = "idle";
  private worker?: Worker;
  private api?: VscodeAPI;
  private negotiatedProtocolVersion?: string;
  private healthCheckInterval?: ReturnType<typeof setInterval>;
  private healthCheckTimeout?: ReturnType<typeof setTimeout>;
  private pendingHealthCheckId?: number;
  private nextHealthCheckId = 1;
  private messageWindowStartedAt = Date.now();
  private messageWindowCount = 0;
  private activationResolve?: () => void;
  private activationReject?: (error: Error) => void;
  private nextCallbackRequestId = 1;
  private nextDisposableId = 1;
  private readonly pendingCallbacks = new Map<
    number,
    { resolve(value: unknown): void; reject(error: Error): void }
  >();
  private readonly pendingLifecycle = new Map<
    number,
    { resolve(): void; reject(error: Error): void }
  >();
  private readonly remoteDisposables = new Map<number, Disposable>();
  private readonly decorationTypes = new Map<number, TextEditorDecorationType>();
  private readonly taskExecutions = new Map<
    number,
    { execution: TaskExecution }
  >();
  private readonly terminals = new Map<number, Terminal>();
  private readonly outputChannels = new Map<number, OutputChannel>();
  private readonly statusBarItems = new Map<number, StatusBarItem>();
  private readonly fileSystemWatchers = new Map<number, FileSystemWatcher>();
  private readonly progressReporters = new Map<number, Progress<{ message?: string; increment?: number }>>();
  private readonly webviewPanels = new Map<number, WebviewPanel>();
  private readonly sourceControls = new Map<number, SourceControl>();
  private readonly sourceControlGroups = new Map<
    number,
    { group: SourceControlResourceGroup; sourceControlHandle: number }
  >();

  constructor(
    extensionId: string,
    mainPath: string,
    source: string,
    onDisposed: () => void,
    onRuntimeFailure: (error: Error) => void,
    runtimePolicy: ExtensionWorkerRuntimePolicy = {},
  ) {
    this.extensionId = extensionId;
    this.mainPath = mainPath;
    this.source = source;
    this.onDisposed = onDisposed;
    this.onRuntimeFailure = onRuntimeFailure;
    this.runtimePolicy = normalizeExtensionWorkerRuntimePolicy(runtimePolicy);
  }

  async activate(api: VscodeAPI): Promise<void> {
    if (this.status === "active") return;
    if (this.status !== "idle") {
      throw new Error(
        "Extension Worker cannot activate from state " + this.status,
      );
    }

    this.status = "activating";
    this.api = api;
    const worker = createExtensionWorker(
      this.extensionId,
      this.mainPath,
      this.source,
    );
    this.worker = worker;
    worker.onmessage = (event: MessageEvent<unknown>) => {
      this.handleWorkerMessage(event.data);
    };
    worker.onerror = (event: ErrorEvent) => {
      this.handleWorkerCrash(
        new Error(event.message || "Extension Worker crashed"),
      );
    };
    worker.onmessageerror = () => {
      this.handleWorkerCrash(
        new Error("Extension Worker message deserialization failed"),
      );
    };

    const activation = new Promise<void>((resolve, reject) => {
      this.activationResolve = resolve;
      this.activationReject = reject;
    });
    const timeout = setTimeout(() => {
      this.failActivation(
        new Error(
          "Extension Worker activation timed out for " + this.extensionId,
        ),
      );
    }, WORKER_ACTIVATION_TIMEOUT_MS);

    worker.postMessage({
      type: "init",
      token: this.token,
      protocolVersions: this.runtimePolicy.offeredProtocolVersions,
      extensionId: this.extensionId,
      mainPath: this.mainPath,
      machineId: api.env.machineId,
      sessionId: api.env.sessionId,
      configuration: api.workspace.getConfigurationSnapshot?.(),
      workspaceFolders: toWorkerSerializable(api.workspace.workspaceFolders),
      activeTextEditor: toWorkerSerializable(api.window.activeTextEditor),
    });

    try {
      await activation;
    } catch (error) {
      this.terminate(error instanceof Error ? error : new Error(String(error)));
      throw error;
    } finally {
      clearTimeout(timeout);
      this.activationResolve = undefined;
      this.activationReject = undefined;
    }
  }

  async deactivate(): Promise<void> {
    if (this.status === "disposed" || this.status === "idle") {
      this.terminate();
      return;
    }
    if (!this.worker) {
      this.terminate();
      return;
    }

    this.status = "deactivating";
    const id = this.nextCallbackRequestId++;
    const response = new Promise<void>((resolve, reject) => {
      this.pendingLifecycle.set(id, { resolve, reject });
    });
    const timeout = setTimeout(() => {
      const pending = this.pendingLifecycle.get(id);
      if (pending) {
        this.pendingLifecycle.delete(id);
        pending.reject(
          new Error(
            "Extension Worker deactivation timed out for " + this.extensionId,
          ),
        );
      }
    }, WORKER_DEACTIVATION_TIMEOUT_MS);
    this.worker.postMessage({
      type: "deactivate",
      token: this.token,
      id,
    });

    try {
      await response;
    } finally {
      clearTimeout(timeout);
      this.terminate();
    }
  }

  terminate(reason = new Error("Extension Worker terminated")): void {
    if (this.status === "disposed") return;
    this.status = "disposed";
    this.stopHealthMonitor();
    this.activationReject?.(reason);
    for (const pending of this.pendingCallbacks.values()) {
      pending.reject(reason);
    }
    this.pendingCallbacks.clear();
    for (const pending of this.pendingLifecycle.values()) {
      pending.reject(reason);
    }
    this.pendingLifecycle.clear();
    for (const executionRecord of this.taskExecutions.values()) {
      try {
        executionRecord.execution.terminate();
      } catch {
        // Host deactivation remains the final cleanup authority.
      }
    }
    this.taskExecutions.clear();
    for (const terminal of this.terminals.values()) {
      try {
        terminal.dispose();
      } catch {
        // Host deactivation remains the final cleanup authority.
      }
    }
    this.terminals.clear();
    for (const channel of this.outputChannels.values()) {
      try {
        channel.dispose();
      } catch {
        // Host deactivation remains the final cleanup authority.
      }
    }
    this.outputChannels.clear();
    for (const item of this.statusBarItems.values()) {
      try { item.dispose(); } catch { /* Worker cleanup remains authoritative. */ }
    }
    this.statusBarItems.clear();
    for (const watcher of this.fileSystemWatchers.values()) {
      try { watcher.dispose(); } catch { /* Worker cleanup remains authoritative. */ }
    }
    this.fileSystemWatchers.clear();
    this.progressReporters.clear();
    for (const panel of this.webviewPanels.values()) {
      try {
        panel.dispose();
      } catch {
        // Host deactivation remains the final cleanup authority.
      }
    }
    this.webviewPanels.clear();
    for (const groupRecord of this.sourceControlGroups.values()) {
      try {
        groupRecord.group.dispose();
      } catch {
        // SourceControl.dispose also releases its groups.
      }
    }
    this.sourceControlGroups.clear();
    for (const sourceControl of this.sourceControls.values()) {
      try {
        sourceControl.dispose();
      } catch {
        // Host deactivation remains the final cleanup authority.
      }
    }
    this.sourceControls.clear();
    for (const disposable of this.remoteDisposables.values()) {
      try {
        disposable.dispose();
      } catch {
        // Host deactivation remains the final cleanup authority.
      }
    }
    this.remoteDisposables.clear();
    this.decorationTypes.clear();
    if (this.worker) {
      const worker = this.worker;
      try {
        worker.postMessage({ type: "terminate", token: this.token });
      } catch {
        // A crashed Worker may no longer accept control messages.
      }
      worker.onmessage = null;
      worker.onerror = null;
      worker.onmessageerror = null;
      worker.terminate();
      this.worker = undefined;
    }
    this.api = undefined;
    this.negotiatedProtocolVersion = undefined;
    this.onDisposed();
  }

  private failActivation(error: Error): void {
    if (this.status !== "activating") return;
    this.activationReject?.(error);
  }

  private handleWorkerCrash(error: Error): void {
    if (this.status === "activating") {
      this.failActivation(error);
      return;
    }
    if (this.status === "deactivating") {
      this.terminate(error);
      return;
    }
    if (this.status === "active") {
      this.terminate(error);
      this.onRuntimeFailure(error);
    }
  }

  private handleWorkerMessage(data: unknown): void {
    if (!isRecord(data) || data.token !== this.token) return;
    if (!this.acceptWorkerMessage(data)) return;
    if (data.type === "protocol-ready") {
      const protocolVersion = data.protocolVersion;
      if (
        this.status !== "activating"
        || typeof protocolVersion !== "string"
        || !this.runtimePolicy.offeredProtocolVersions.includes(protocolVersion)
        || !EXTENSION_WORKER_PROTOCOL_VERSIONS.includes(
          protocolVersion as (typeof EXTENSION_WORKER_PROTOCOL_VERSIONS)[number],
        )
      ) {
        this.handleProtocolViolation("Extension Worker selected an invalid protocol version");
        return;
      }
      if (
        this.negotiatedProtocolVersion
        && this.negotiatedProtocolVersion !== protocolVersion
      ) {
        this.handleProtocolViolation("Extension Worker renegotiated its protocol version");
        return;
      }
      this.negotiatedProtocolVersion = protocolVersion;
      return;
    }
    if (data.type === "protocol-error") {
      this.handleProtocolViolation(
        typeof data.error === "string"
          ? data.error
          : "Extension Worker protocol negotiation failed",
      );
      return;
    }
    if (!this.negotiatedProtocolVersion) {
      this.handleProtocolViolation(
        "Extension Worker sent a message before protocol negotiation",
      );
      return;
    }
    if (data.type === "health-response") {
      this.handleHealthResponse(data);
      return;
    }
    switch (data.type) {
      case "activated":
        if (this.status === "activating") {
          this.status = "active";
          this.startHealthMonitor();
          this.activationResolve?.();
        }
        break;
      case "activation-error":
        this.failActivation(
          new Error(
            typeof data.error === "string"
              ? data.error
              : "Extension Worker activation failed",
          ),
        );
        break;
      case "runtime-error":
        this.handleWorkerCrash(
          new Error(
            typeof data.error === "string"
              ? data.error
              : "Extension Worker crashed",
          ),
        );
        break;
      case "rpc":
        void this.handleWorkerRPC(data).catch(() => undefined);
        break;
      case "callback-result":
        this.handleCallbackResult(data);
        break;
      case "deactivated":
        this.handleDeactivated(data);
        break;
    }
  }

  private acceptWorkerMessage(data: Record<string, unknown>): boolean {
    if (
      boundedStructuredSize(data, this.runtimePolicy.maxMessageBytes)
      > this.runtimePolicy.maxMessageBytes
    ) {
      this.handleProtocolViolation(
        `Extension Worker message exceeds ${this.runtimePolicy.maxMessageBytes} bytes`,
      );
      return false;
    }
    const now = Date.now();
    if (now - this.messageWindowStartedAt >= 1_000) {
      this.messageWindowStartedAt = now;
      this.messageWindowCount = 0;
    }
    this.messageWindowCount += 1;
    if (this.messageWindowCount > this.runtimePolicy.maxMessagesPerSecond) {
      this.handleProtocolViolation(
        `Extension Worker exceeded ${this.runtimePolicy.maxMessagesPerSecond} messages per second`,
      );
      return false;
    }
    return true;
  }

  private handleProtocolViolation(message: string): void {
    const error = new Error(message);
    if (this.status === "active") {
      this.handleWorkerCrash(error);
      return;
    }
    if (this.status === "activating") {
      this.failActivation(error);
      this.terminate(error);
    }
  }

  private startHealthMonitor(): void {
    if (this.healthCheckInterval !== undefined) return;
    this.sendHealthCheck();
    this.healthCheckInterval = setInterval(
      () => this.sendHealthCheck(),
      this.runtimePolicy.healthCheckIntervalMs,
    );
  }

  private sendHealthCheck(): void {
    if (this.status !== "active" || !this.worker || this.pendingHealthCheckId !== undefined) {
      return;
    }
    const id = this.nextHealthCheckId++;
    this.pendingHealthCheckId = id;
    this.healthCheckTimeout = setTimeout(() => {
      if (this.pendingHealthCheckId !== id || this.status !== "active") return;
      this.pendingHealthCheckId = undefined;
      this.healthCheckTimeout = undefined;
      this.handleWorkerCrash(
        new Error(`Extension Worker health check timed out for ${this.extensionId}`),
      );
    }, this.runtimePolicy.healthCheckTimeoutMs);
    this.worker.postMessage({ type: "health-check", token: this.token, id });
  }

  private handleHealthResponse(data: Record<string, unknown>): void {
    if (
      typeof data.id !== "number"
      || data.id !== this.pendingHealthCheckId
      || this.status !== "active"
    ) {
      return;
    }
    this.pendingHealthCheckId = undefined;
    if (this.healthCheckTimeout !== undefined) {
      clearTimeout(this.healthCheckTimeout);
      this.healthCheckTimeout = undefined;
    }
  }

  private stopHealthMonitor(): void {
    if (this.healthCheckInterval !== undefined) {
      clearInterval(this.healthCheckInterval);
      this.healthCheckInterval = undefined;
    }
    if (this.healthCheckTimeout !== undefined) {
      clearTimeout(this.healthCheckTimeout);
      this.healthCheckTimeout = undefined;
    }
    this.pendingHealthCheckId = undefined;
  }

  private storeRemoteDisposable(disposable: Disposable): number {
    const disposableId = this.nextDisposableId++;
    this.remoteDisposables.set(disposableId, disposable);
    return disposableId;
  }

  private createWorkerProvider(
    callbackMap: Record<string, number>,
  ): Record<string, (...args: unknown[]) => Promise<unknown>> {
    const provider: Record<string, (...args: unknown[]) => Promise<unknown>> =
      Object.create(null);
    for (const [methodName, callbackId] of Object.entries(callbackMap)) {
      provider[methodName] = (...args: unknown[]) =>
        this.invokeWorkerCallback(callbackId, args);
    }
    return provider;
  }

  private workerProviderMethod<T extends (...args: never[]) => unknown>(
    provider: Record<string, (...args: unknown[]) => Promise<unknown>>,
    methodName: string,
  ): (...args: Parameters<T>) => ReturnType<T> {
    const callback = provider[methodName];
    if (!callback) {
      throw new Error(`Worker provider is missing callback "${methodName}"`);
    }
    // The Worker RPC promise is the Thenable branch of every provider API.
    // The host-side provider interface remains exact while the extension owns
    // validation of the result shape, matching VS Code's provider contract.
    return (...args: Parameters<T>) => callback(...args) as ReturnType<T>;
  }

  private registerWorkerLanguageProvider(
    api: VscodeAPI,
    kind: LanguageProviderKind,
    selector: DocumentSelector,
    provider: Record<string, (...args: unknown[]) => Promise<unknown>>,
    extra: unknown,
  ): Disposable {
    switch (kind) {
      case "completion": {
        type Provider = Parameters<
          typeof api.languages.registerCompletionItemProvider
        >[1];
        return api.languages.registerCompletionItemProvider(selector, {
          provideCompletionItems: this.workerProviderMethod<
            Provider["provideCompletionItems"]
          >(provider, "provideCompletionItems"),
        });
      }
      case "hover": {
        type Provider = Parameters<typeof api.languages.registerHoverProvider>[1];
        return api.languages.registerHoverProvider(selector, {
          provideHover: this.workerProviderMethod<Provider["provideHover"]>(
            provider,
            "provideHover",
          ),
        });
      }
      case "definition": {
        type Provider = Parameters<
          typeof api.languages.registerDefinitionProvider
        >[1];
        return api.languages.registerDefinitionProvider(selector, {
          provideDefinition: this.workerProviderMethod<Provider["provideDefinition"]>(
            provider,
            "provideDefinition",
          ),
        });
      }
      case "codeAction": {
        type Provider = Parameters<
          typeof api.languages.registerCodeActionProvider
        >[1];
        return api.languages.registerCodeActionProvider(selector, {
          provideCodeActions: this.workerProviderMethod<Provider["provideCodeActions"]>(
            provider,
            "provideCodeActions",
          ),
        });
      }
      case "reference": {
        type Provider = Parameters<
          typeof api.languages.registerReferenceProvider
        >[1];
        return api.languages.registerReferenceProvider(selector, {
          provideReferences: this.workerProviderMethod<Provider["provideReferences"]>(
            provider,
            "provideReferences",
          ),
        });
      }
      case "codeLens": {
        type Provider = Parameters<
          typeof api.languages.registerCodeLensProvider
        >[1];
        return api.languages.registerCodeLensProvider(selector, {
          provideCodeLenses: this.workerProviderMethod<Provider["provideCodeLenses"]>(
            provider,
            "provideCodeLenses",
          ),
        });
      }
      case "documentFormatting": {
        type Provider = Parameters<
          typeof api.languages.registerDocumentFormattingEditProvider
        >[1];
        return api.languages.registerDocumentFormattingEditProvider(selector, {
          provideDocumentFormattingEdits: this.workerProviderMethod<
            Provider["provideDocumentFormattingEdits"]
          >(provider, "provideDocumentFormattingEdits"),
        });
      }
      case "documentRangeFormatting": {
        type Provider = Parameters<
          typeof api.languages.registerDocumentRangeFormattingEditProvider
        >[1];
        return api.languages.registerDocumentRangeFormattingEditProvider(selector, {
          provideDocumentRangeFormattingEdits: this.workerProviderMethod<
            Provider["provideDocumentRangeFormattingEdits"]
          >(provider, "provideDocumentRangeFormattingEdits"),
        });
      }
      case "onTypeFormatting": {
        type Provider = Parameters<
          typeof api.languages.registerOnTypeFormattingEditProvider
        >[1];
        const options = isRecord(extra) ? extra : {};
        const first = typeof options.firstTriggerCharacter === "string"
          ? options.firstTriggerCharacter
          : "";
        const more = isStringArray(options.moreTriggerCharacter)
          ? options.moreTriggerCharacter
          : undefined;
        return api.languages.registerOnTypeFormattingEditProvider(
          selector,
          {
            provideOnTypeFormattingEdits: this.workerProviderMethod<
              Provider["provideOnTypeFormattingEdits"]
            >(provider, "provideOnTypeFormattingEdits"),
          },
          first,
          more,
        );
      }
      case "signatureHelp": {
        type Provider = Parameters<
          typeof api.languages.registerSignatureHelpProvider
        >[1];
        return api.languages.registerSignatureHelpProvider(selector, {
          provideSignatureHelp: this.workerProviderMethod<
            Provider["provideSignatureHelp"]
          >(provider, "provideSignatureHelp"),
        });
      }
      case "workspaceSymbol": {
        type Provider = Parameters<
          typeof api.languages.registerWorkspaceSymbolProvider
        >[0];
        return api.languages.registerWorkspaceSymbolProvider({
          provideWorkspaceSymbols: this.workerProviderMethod<
            Provider["provideWorkspaceSymbols"]
          >(provider, "provideWorkspaceSymbols"),
        });
      }
      case "documentLink": {
        type Provider = Parameters<
          typeof api.languages.registerDocumentLinkProvider
        >[1];
        const resolveDocumentLink = provider.resolveDocumentLink
          ? this.workerProviderMethod<NonNullable<Provider["resolveDocumentLink"]>>(
              provider,
              "resolveDocumentLink",
            )
          : undefined;
        return api.languages.registerDocumentLinkProvider(selector, {
          provideDocumentLinks: this.workerProviderMethod<Provider["provideDocumentLinks"]>(
            provider,
            "provideDocumentLinks",
          ),
          ...(resolveDocumentLink ? { resolveDocumentLink } : {}),
        });
      }
      case "color": {
        type Provider = Parameters<typeof api.languages.registerColorProvider>[1];
        return api.languages.registerColorProvider(selector, {
          provideDocumentColors: this.workerProviderMethod<
            Provider["provideDocumentColors"]
          >(provider, "provideDocumentColors"),
          provideColorPresentations: this.workerProviderMethod<
            Provider["provideColorPresentations"]
          >(provider, "provideColorPresentations"),
        });
      }
      case "foldingRange": {
        type Provider = Parameters<
          typeof api.languages.registerFoldingRangeProvider
        >[1];
        return api.languages.registerFoldingRangeProvider(selector, {
          provideFoldingRanges: this.workerProviderMethod<Provider["provideFoldingRanges"]>(
            provider,
            "provideFoldingRanges",
          ),
        });
      }
      case "declaration": {
        type Provider = Parameters<
          typeof api.languages.registerDeclarationProvider
        >[1];
        return api.languages.registerDeclarationProvider(selector, {
          provideDeclaration: this.workerProviderMethod<Provider["provideDeclaration"]>(
            provider,
            "provideDeclaration",
          ),
        });
      }
      case "implementation": {
        type Provider = Parameters<
          typeof api.languages.registerImplementationProvider
        >[1];
        return api.languages.registerImplementationProvider(selector, {
          provideImplementation: this.workerProviderMethod<
            Provider["provideImplementation"]
          >(provider, "provideImplementation"),
        });
      }
      case "typeDefinition": {
        type Provider = Parameters<
          typeof api.languages.registerTypeDefinitionProvider
        >[1];
        return api.languages.registerTypeDefinitionProvider(selector, {
          provideTypeDefinition: this.workerProviderMethod<
            Provider["provideTypeDefinition"]
          >(provider, "provideTypeDefinition"),
        });
      }
      case "rename": {
        type Provider = Parameters<typeof api.languages.registerRenameProvider>[1];
        const prepareRename = provider.prepareRename
          ? this.workerProviderMethod<NonNullable<Provider["prepareRename"]>>(
              provider,
              "prepareRename",
            )
          : undefined;
        return api.languages.registerRenameProvider(selector, {
          provideRenameEdits: this.workerProviderMethod<Provider["provideRenameEdits"]>(
            provider,
            "provideRenameEdits",
          ),
          ...(prepareRename ? { prepareRename } : {}),
        });
      }
      case "documentSymbol": {
        type Provider = Parameters<
          typeof api.languages.registerDocumentSymbolProvider
        >[1];
        return api.languages.registerDocumentSymbolProvider(selector, {
          provideDocumentSymbols: this.workerProviderMethod<
            Provider["provideDocumentSymbols"]
          >(provider, "provideDocumentSymbols"),
        });
      }
      case "documentSemanticTokens": {
        type Provider = Parameters<
          typeof api.languages.registerDocumentSemanticTokensProvider
        >[1];
        const provideDocumentSemanticTokensEdits =
          provider.provideDocumentSemanticTokensEdits
            ? this.workerProviderMethod<
                NonNullable<Provider["provideDocumentSemanticTokensEdits"]>
              >(provider, "provideDocumentSemanticTokensEdits")
            : undefined;
        return api.languages.registerDocumentSemanticTokensProvider(selector, {
          provideDocumentSemanticTokens: this.workerProviderMethod<
            Provider["provideDocumentSemanticTokens"]
          >(provider, "provideDocumentSemanticTokens"),
          ...(provideDocumentSemanticTokensEdits
            ? { provideDocumentSemanticTokensEdits }
            : {}),
        });
      }
      case "documentRangeSemanticTokens": {
        type Provider = Parameters<
          typeof api.languages.registerDocumentRangeSemanticTokensProvider
        >[1];
        return api.languages.registerDocumentRangeSemanticTokensProvider(selector, {
          provideDocumentRangeSemanticTokens: this.workerProviderMethod<
            Provider["provideDocumentRangeSemanticTokens"]
          >(provider, "provideDocumentRangeSemanticTokens"),
        });
      }
      case "documentHighlight": {
        type Provider = Parameters<
          typeof api.languages.registerDocumentHighlightProvider
        >[1];
        return api.languages.registerDocumentHighlightProvider(selector, {
          provideDocumentHighlights: this.workerProviderMethod<
            Provider["provideDocumentHighlights"]
          >(provider, "provideDocumentHighlights"),
        });
      }
      case "inlayHints": {
        type Provider = Parameters<
          typeof api.languages.registerInlayHintsProvider
        >[1];
        return api.languages.registerInlayHintsProvider(selector, {
          provideInlayHints: this.workerProviderMethod<Provider["provideInlayHints"]>(
            provider,
            "provideInlayHints",
          ),
        });
      }
    }
  }

  private async handleWorkerRPC(message: Record<string, unknown>): Promise<void> {
    const id = message.id;
    const method = message.method;
    const args = Array.isArray(message.args) ? message.args : [];
    if (typeof id !== "number" || typeof method !== "string") return;
    try {
      let result: unknown;
      if (method === "commands.registerCommand") {
        if (this.status !== "activating" && this.status !== "active") {
          throw new Error("Late command registration was rejected");
        }
        const command = args[0];
        const callbackId = args[1];
        if (typeof command !== "string" || typeof callbackId !== "number") {
          throw new Error("Invalid commands.registerCommand request");
        }
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        const disposable = api.commands.registerCommand(
          command,
          (...callbackArgs: unknown[]) =>
            this.invokeWorkerCallback(callbackId, callbackArgs),
        );
        const disposableId = this.nextDisposableId++;
        this.remoteDisposables.set(disposableId, disposable);
        result = disposableId;
      } else if (method === "commands.executeCommand") {
        if (this.status !== "activating" && this.status !== "active") {
          throw new Error("Extension command execution is unavailable");
        }
        const command = args[0];
        if (typeof command !== "string") {
          throw new Error("Invalid commands.executeCommand request");
        }
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        result = await api.commands.executeCommand(command, ...args.slice(1));
      } else if (method === "commands.dispose") {
        const disposableId = args[0];
        if (typeof disposableId !== "number") {
          throw new Error("Invalid commands.dispose request");
        }
        this.remoteDisposables.get(disposableId)?.dispose();
        this.remoteDisposables.delete(disposableId);
      } else if (method === "disposables.dispose") {
        const disposableId = args[0];
        if (typeof disposableId !== "number") {
          throw new Error("Invalid disposable handle");
        }
        this.remoteDisposables.get(disposableId)?.dispose();
        this.remoteDisposables.delete(disposableId);
        this.decorationTypes.delete(disposableId);
        this.fileSystemWatchers.delete(disposableId);
      } else if (method === "workspace.createFileSystemWatcher") {
        if (this.status !== "activating" && this.status !== "active") {
          throw new Error("Late file watcher registration was rejected");
        }
        const globPattern = args[0];
        if (!isGlobPattern(globPattern)) {
          throw new Error("Invalid workspace.createFileSystemWatcher request");
        }
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        const watcher = api.workspace.createFileSystemWatcher(globPattern);
        const handleId = this.nextDisposableId++;
        this.fileSystemWatchers.set(handleId, watcher);
        this.remoteDisposables.set(handleId, watcher);
        result = handleId;
      } else if (
        method === "workspace.watcher.onDidCreate"
        || method === "workspace.watcher.onDidChange"
        || method === "workspace.watcher.onDidDelete"
      ) {
        const handleId = args[0];
        const callbackMap = args[1];
        if (typeof handleId !== "number" || !isCallbackMap(callbackMap) || typeof callbackMap.listener !== "number") {
          throw new Error("Invalid workspace watcher listener request");
        }
        const watcher = this.fileSystemWatchers.get(handleId);
        if (!watcher) throw new Error("Unknown file watcher handle");
        const event = method === "workspace.watcher.onDidCreate"
          ? watcher.onDidCreate
          : method === "workspace.watcher.onDidChange"
            ? watcher.onDidChange
            : watcher.onDidDelete;
        result = this.storeRemoteDisposable(
          event((uri) => {
            void this.invokeWorkerCallback(callbackMap.listener, [uri]).catch(() => undefined);
          }),
        );
      } else if (method === "workspace.watcher.dispose") {
        const handleId = args[0];
        if (typeof handleId !== "number") throw new Error("Invalid file watcher handle");
        this.fileSystemWatchers.get(handleId)?.dispose();
        this.fileSystemWatchers.delete(handleId);
        this.remoteDisposables.delete(handleId);
      } else if (method === "languages.registerProvider") {
        if (this.status !== "activating" && this.status !== "active") {
          throw new Error("Late language provider registration was rejected");
        }
        const kind = args[0];
        const selector = args[1];
        const extra = args[2];
        const callbackMap = args[3];
        if (
          typeof kind !== "string"
          || !LANGUAGE_PROVIDER_KINDS.has(kind as LanguageProviderKind)
          || !isDocumentSelector(selector)
          || !isCallbackMap(callbackMap)
        ) {
          throw new Error("Invalid languages.registerProvider request");
        }
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        result = this.storeRemoteDisposable(
          this.registerWorkerLanguageProvider(
            api,
            kind as LanguageProviderKind,
            selector,
            this.createWorkerProvider(callbackMap),
            extra,
          ),
        );
      } else if (method.startsWith("workspace.fs.")) {
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        if (method === "workspace.fs.readFile") {
          if (!isWorkerUri(args[0])) throw new Error("Invalid file URI");
          result = await api.workspace.fs.readFile(args[0]);
        } else if (method === "workspace.fs.writeFile") {
          const content = normalizeWorkerBytes(args[1]);
          if (!isWorkerUri(args[0]) || !content) {
            throw new Error("Invalid workspace.fs.writeFile request");
          }
          await api.workspace.fs.writeFile(args[0], content);
        } else if (method === "workspace.fs.exists") {
          if (!isWorkerUri(args[0])) throw new Error("Invalid file URI");
          result = await api.workspace.fs.exists(args[0]);
        } else if (method === "workspace.fs.createDirectory") {
          if (!isWorkerUri(args[0])) throw new Error("Invalid file URI");
          await api.workspace.fs.createDirectory(args[0]);
        } else if (method === "workspace.fs.rename") {
          if (!isWorkerUri(args[0]) || !isWorkerUri(args[1])) {
            throw new Error("Invalid workspace.fs.rename request");
          }
          const options = args[2];
          if (
            options !== undefined
            && (!isRecord(options)
              || (options.overwrite !== undefined
                && typeof options.overwrite !== "boolean"))
          ) {
            throw new Error("Invalid workspace.fs.rename options");
          }
          await api.workspace.fs.rename(
            args[0],
            args[1],
            options as Parameters<typeof api.workspace.fs.rename>[2],
          );
        } else if (method === "workspace.fs.delete") {
          if (!isWorkerUri(args[0])) throw new Error("Invalid file URI");
          const options = args[1];
          if (
            options !== undefined
            && (!isRecord(options)
              || (options.recursive !== undefined
                && typeof options.recursive !== "boolean")
              || (options.useTrash !== undefined
                && typeof options.useTrash !== "boolean"))
          ) {
            throw new Error("Invalid workspace.fs.delete options");
          }
          await api.workspace.fs.delete(
            args[0],
            options as Parameters<typeof api.workspace.fs.delete>[1],
          );
        } else if (method === "workspace.fs.readDirectory") {
          if (!isWorkerUri(args[0])) throw new Error("Invalid file URI");
          result = await api.workspace.fs.readDirectory(args[0]);
        } else {
          throw new Error("Unsupported workspace filesystem method: " + method);
        }
      } else if (method === "workspace.getConfiguration") {
        const section = args[0];
        if (section !== undefined && typeof section !== "string") {
          throw new Error("Invalid workspace.getConfiguration request");
        }
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        result = api.workspace.getConfigurationSnapshot?.(section) ?? {};
      } else if (method === "workspace.findFiles") {
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        if (
          !isGlobPattern(args[0])
          || (args[1] !== undefined && !isGlobPattern(args[1]))
          || (args[2] !== undefined && typeof args[2] !== "number")
        ) {
          throw new Error("Invalid workspace.findFiles request");
        }
        result = await api.workspace.findFiles(
          args[0],
          args[1],
          args[2],
        );
      } else if (method === "workspace.findTextInFiles") {
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        const query = args[0];
        const options = args[1];
        if (
          !isRecord(query)
          || typeof query.pattern !== "string"
          || !isRecord(options)
          || (options.include !== undefined && !isGlobPattern(options.include))
          || (options.exclude !== undefined && !isGlobPattern(options.exclude))
          || (options.maxResults !== undefined && typeof options.maxResults !== "number")
          || (options.ignoreCase !== undefined && typeof options.ignoreCase !== "boolean")
        ) {
          throw new Error("Invalid workspace.findTextInFiles request");
        }
        const normalizedQuery: TextSearchQuery = {
          pattern: query.pattern,
          ...(typeof query.isRegExp === "boolean" ? { isRegExp: query.isRegExp } : {}),
          ...(typeof query.isCaseSensitive === "boolean"
            ? { isCaseSensitive: query.isCaseSensitive }
            : {}),
          ...(typeof query.isWordMatch === "boolean"
            ? { isWordMatch: query.isWordMatch }
            : {}),
        };
        const normalizedOptions: FindTextInFilesOptions = {
          ...(options.include !== undefined ? { include: options.include } : {}),
          ...(options.exclude !== undefined ? { exclude: options.exclude } : {}),
          ...(typeof options.maxResults === "number"
            ? { maxResults: options.maxResults }
            : {}),
          ...(typeof options.ignoreCase === "boolean"
            ? { ignoreCase: options.ignoreCase }
            : {}),
        };
        result = await api.workspace.findTextInFiles(
          normalizedQuery,
          normalizedOptions,
        );
      } else if (method === "workspace.openTextDocument") {
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        if (!isWorkerUri(args[0])) throw new Error("Invalid file URI");
        result = serializeTextDocument(await api.workspace.openTextDocument(args[0]));
      } else if (method === "workspace.saveAll") {
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        if (args[0] !== undefined && typeof args[0] !== "boolean") {
          throw new Error("Invalid workspace.saveAll request");
        }
        result = await api.workspace.saveAll(args[0]);
      } else if (
        method === "workspace.onDidSaveTextDocument"
        || method === "workspace.onDidChangeTextDocument"
        || method === "workspace.onDidOpenTextDocument"
        || method === "workspace.onDidCloseTextDocument"
        || method === "workspace.onDidChangeConfiguration"
        || method === "window.onDidChangeActiveTextEditor"
        || method === "window.onDidChangeTextEditorSelection"
      ) {
        if (this.status !== "activating" && this.status !== "active") {
          throw new Error("Late workspace listener registration was rejected");
        }
        const callbackMap = args[0];
        if (!isCallbackMap(callbackMap) || typeof callbackMap.listener !== "number") {
          throw new Error("Invalid workspace listener request");
        }
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        const listener = (event: unknown) =>
          this.invokeWorkerCallback(callbackMap.listener, [event]);
        const disposable = method === "workspace.onDidSaveTextDocument"
          ? api.workspace.onDidSaveTextDocument(listener)
          : method === "workspace.onDidChangeTextDocument"
            ? api.workspace.onDidChangeTextDocument(
                listener as Parameters<typeof api.workspace.onDidChangeTextDocument>[0],
              )
            : method === "workspace.onDidOpenTextDocument"
              ? api.workspace.onDidOpenTextDocument(listener)
              : method === "workspace.onDidCloseTextDocument"
                ? api.workspace.onDidCloseTextDocument(listener)
                : method === "window.onDidChangeActiveTextEditor"
                  ? api.window.onDidChangeActiveTextEditor(listener)
                  : method === "window.onDidChangeTextEditorSelection"
                    ? api.window.onDidChangeTextEditorSelection(
                        listener as Parameters<typeof api.window.onDidChangeTextEditorSelection>[0],
                      )
                    : api.workspace.onDidChangeConfiguration(listener as Parameters<typeof api.workspace.onDidChangeConfiguration>[0]);
        result = this.storeRemoteDisposable(disposable);
      } else if (method === "secrets.get") {
        const key = args[0];
        if (typeof key !== "string" || key.length === 0) {
          throw new Error("Invalid secrets.get request");
        }
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        result = await api.secrets.get(key);
      } else if (method === "secrets.store") {
        const key = args[0];
        const value = args[1];
        if (
          typeof key !== "string" ||
          key.length === 0 ||
          typeof value !== "string"
        ) {
          throw new Error("Invalid secrets.store request");
        }
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        await api.secrets.store(key, value);
      } else if (method === "secrets.delete") {
        const key = args[0];
        if (typeof key !== "string" || key.length === 0) {
          throw new Error("Invalid secrets.delete request");
        }
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        await api.secrets.delete(key);
      } else if (method === "tasks.registerTaskProvider") {
        if (this.status !== "activating" && this.status !== "active") {
          throw new Error("Late task provider registration was rejected");
        }
        const type = args[0];
        const callbackMap = args[1];
        if (
          typeof type !== "string"
          || type.trim().length === 0
          || !isCallbackMap(callbackMap)
        ) {
          throw new Error("Invalid tasks.registerTaskProvider request");
        }
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        type Provider = Parameters<typeof api.tasks.registerTaskProvider>[1];
        const workerProvider = this.createWorkerProvider(callbackMap);
        result = this.storeRemoteDisposable(
          api.tasks.registerTaskProvider(
            type,
            {
              provideTasks: this.workerProviderMethod<Provider["provideTasks"]>(
                workerProvider,
                "provideTasks",
              ),
              resolveTask: this.workerProviderMethod<Provider["resolveTask"]>(
                workerProvider,
                "resolveTask",
              ),
            },
          ),
        );
      } else if (method === "tasks.fetchTasks") {
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        result = await api.tasks.fetchTasks();
      } else if (method === "tasks.executeTask") {
        const task = args[0];
        if (!isWorkerTask(task)) {
          throw new Error("Invalid tasks.executeTask request");
        }
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        const execution = await api.tasks.executeTask(task);
        const handleId = this.nextDisposableId++;
        const executionRecord = { execution };
        this.taskExecutions.set(handleId, executionRecord);
        if (execution.completion) {
          void execution.completion.then(() => {
            if (this.taskExecutions.get(handleId) === executionRecord) {
              this.taskExecutions.delete(handleId);
              try {
                this.postToWorker({ type: "task-completed", handleId });
              } catch {
                // The Worker may already be closing; the local handle is gone.
              }
            }
          });
        }
        result = handleId;
      } else if (method === "tasks.terminate") {
        const handleId = args[0];
        if (typeof handleId !== "number") {
          throw new Error("Invalid tasks.terminate request");
        }
        const executionRecord = this.taskExecutions.get(handleId);
        if (!executionRecord) throw new Error("Unknown task execution handle");
        executionRecord.execution.terminate();
        this.taskExecutions.delete(handleId);
        this.postToWorker({ type: "task-completed", handleId });
      } else if (method === "window.createTextEditorDecorationType") {
        if (this.status !== "activating" && this.status !== "active") {
          throw new Error("Late decoration registration was rejected");
        }
        const options = args[0];
        if (!isRecord(options)) {
          throw new Error("Invalid window.createTextEditorDecorationType request");
        }
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        const decorationType = api.window.createTextEditorDecorationType(options);
        const handleId = this.nextDisposableId++;
        this.decorationTypes.set(handleId, decorationType);
        this.remoteDisposables.set(handleId, decorationType);
        result = handleId;
      } else if (method === "window.textEditor.setSelection") {
        const selection = args[0];
        const documentUri = args[1];
        if (!isWorkerSelection(selection)) {
          throw new Error("Invalid text editor selection");
        }
        if (documentUri !== undefined && !isWorkerUri(documentUri)) {
          throw new Error("Invalid selection document URI");
        }
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        const editor = api.window.activeTextEditor;
        if (!editor) throw new Error("No active text editor is available for selection");
        if (documentUri && editor.document.uri.fsPath !== documentUri.fsPath) {
          throw new Error("Active editor does not match selection document");
        }
        editor.selection = selection as import("@/lib/extensionHost/vscodeApi").Selection;
      } else if (method === "window.textEditor.revealRange") {
        const range = args[0];
        const revealType = args[1];
        const documentUri = args[2];
        if (!isWorkerRange(range) || !Number.isInteger(revealType) || Number(revealType) < 0 || Number(revealType) > 3) {
          throw new Error("Invalid text editor reveal request");
        }
        if (documentUri !== undefined && !isWorkerUri(documentUri)) {
          throw new Error("Invalid reveal document URI");
        }
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        const editor = api.window.activeTextEditor;
        if (!editor) throw new Error("No active text editor is available for reveal");
        if (documentUri && editor.document.uri.fsPath !== documentUri.fsPath) {
          throw new Error("Active editor does not match reveal document");
        }
        editor.revealRange(
          range as import("@/lib/extensionHost/vscodeApi").Range,
          revealType as number,
        );
      } else if (method === "window.textEditor.setDecorations") {
        const handleId = args[0];
        const ranges = args[1];
        const documentUri = args[2];
        if (typeof handleId !== "number" || !Array.isArray(ranges)) {
          throw new Error("Invalid window.textEditor.setDecorations request");
        }
        if (documentUri !== undefined && !isWorkerUri(documentUri)) {
          throw new Error("Invalid decoration document URI");
        }
        const decorationType = this.decorationTypes.get(handleId);
        if (!decorationType) throw new Error("Unknown text editor decoration handle");
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        const editor = api.window.activeTextEditor;
        if (!editor) throw new Error("No active text editor is available for decorations");
        if (documentUri && editor.document.uri.fsPath !== documentUri.fsPath) {
          throw new Error("Active editor does not match decoration document");
        }
        editor.setDecorations(
          decorationType,
          ranges as Parameters<TextEditor["setDecorations"]>[1],
        );
      } else if (method === "window.createWebviewPanel") {
        const viewType = args[0];
        const title = args[1];
        if (typeof viewType !== "string" || typeof title !== "string") {
          throw new Error("Invalid window.createWebviewPanel request");
        }
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        const panel = api.window.createWebviewPanel(
          viewType,
          title,
          args[2],
          args[3],
        );
        const handleId = this.nextDisposableId++;
        this.webviewPanels.set(handleId, panel);
        result = handleId;
      } else if (method === "window.webview.setHtml") {
        const handleId = args[0];
        const html = args[1];
        if (typeof handleId !== "number" || typeof html !== "string") {
          throw new Error("Invalid webview HTML request");
        }
        const panel = this.webviewPanels.get(handleId);
        if (!panel) throw new Error("Unknown webview panel handle");
        panel.webview.html = html;
      } else if (method === "window.webview.dispose") {
        const handleId = args[0];
        if (typeof handleId !== "number") {
          throw new Error("Invalid webview panel handle");
        }
        this.webviewPanels.get(handleId)?.dispose();
        this.webviewPanels.delete(handleId);
      } else if (method === "window.webview.onDidDispose") {
        const handleId = args[0];
        const callbackId = args[1];
        if (typeof handleId !== "number" || typeof callbackId !== "number") {
          throw new Error("Invalid webview dispose listener request");
        }
        const panel = this.webviewPanels.get(handleId);
        if (!panel) throw new Error("Unknown webview panel handle");
        result = this.storeRemoteDisposable(
          panel.onDidDispose(() => {
            void this.invokeWorkerCallback(callbackId, []).catch(() => undefined);
          }),
        );
      } else if (method === "window.showMessage") {
        const level = args[0];
        const messageText = args[1];
        const items = args[2];
        if (
          (level !== "info" && level !== "warn" && level !== "error")
          || typeof messageText !== "string"
          || !isStringArray(items)
        ) {
          throw new Error("Invalid window.showMessage request");
        }
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        result = level === "info"
          ? await api.window.showInformationMessage(messageText, ...items)
          : level === "warn"
            ? await api.window.showWarningMessage(messageText, ...items)
            : await api.window.showErrorMessage(messageText, ...items);
      } else if (method === "window.showInputBox") {
        const rawOptions = args[0];
        const validateCallbackId = args[1];
        if (rawOptions !== undefined && !isRecord(rawOptions)) {
          throw new Error("Invalid window.showInputBox options");
        }
        if (validateCallbackId !== undefined && typeof validateCallbackId !== "number") {
          throw new Error("Invalid window.showInputBox validator callback");
        }
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        const options: Parameters<typeof api.window.showInputBox>[0] = rawOptions
          ? {
              ...(rawOptions as Parameters<typeof api.window.showInputBox>[0]),
              ...(typeof validateCallbackId === "number"
                ? {
                    validateInput: (value: string) =>
                      this.invokeWorkerCallback(validateCallbackId, [value]) as Promise<string | undefined | null>,
                  }
                : {}),
            }
          : undefined;
        result = await api.window.showInputBox(options);
      } else if (method === "window.showQuickPick") {
        if (!Array.isArray(args[0])) {
          throw new Error("Invalid window.showQuickPick request");
        }
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        result = await api.window.showQuickPick(
          args[0] as Parameters<typeof api.window.showQuickPick>[0],
          args[1] as Parameters<typeof api.window.showQuickPick>[1],
        );
      } else if (method === "window.setStatusBarMessage") {
        if (typeof args[0] !== "string") throw new Error("Invalid status bar message");
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        const disposable = api.window.setStatusBarMessage(args[0], typeof args[1] === "number" ? args[1] : undefined);
        const handleId = this.nextDisposableId++;
        this.remoteDisposables.set(handleId, disposable);
        result = handleId;
      } else if (method === "window.createStatusBarItem") {
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        const item = api.window.createStatusBarItem();
        const handleId = this.nextDisposableId++;
        this.remoteDisposables.set(handleId, item);
        this.statusBarItems.set(handleId, item);
        result = handleId;
      } else if (method.startsWith("window.status.")) {
        const handleId = args[0];
        if (typeof handleId !== "number") throw new Error("Invalid status bar handle");
        const item = this.statusBarItems.get(handleId) ?? this.remoteDisposables.get(handleId);
        if (!item) throw new Error("Unknown status bar handle");
        if (method === "window.status.show") {
          (item as StatusBarItem).show?.();
        } else if (method === "window.status.hide") {
          (item as StatusBarItem).hide?.();
        } else if (method === "window.status.setText") {
          if (typeof args[1] !== "string") throw new Error("Invalid status text");
          (item as StatusBarItem).text = args[1];
        } else if (method === "window.status.setTooltip") {
          (item as StatusBarItem).tooltip = typeof args[1] === "string" ? args[1] : undefined;
        } else if (method === "window.status.setCommand") {
          (item as StatusBarItem).command = typeof args[1] === "string" ? args[1] : undefined;
        } else if (method === "window.status.dispose") {
          item.dispose();
          this.statusBarItems.delete(handleId);
          this.remoteDisposables.delete(handleId);
        } else {
          throw new Error("Unsupported status bar method: " + method);
        }
      } else if (method === "window.withProgress") {
        const options = args[0];
        const callbackId = args[1];
        if (typeof callbackId !== "number") throw new Error("Invalid progress callback");
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        result = await api.window.withProgress(
          options as Parameters<typeof api.window.withProgress>[0],
          (progress) => {
            this.progressReporters.set(callbackId, progress);
            return this.invokeWorkerCallback(callbackId, [
              { __koyoriIdeType: "Progress", id: callbackId },
            ]).finally(() => this.progressReporters.delete(callbackId));
          },
        );
      } else if (method === "window.progress.report") {
        const progressId = args[0];
        const value = args[1];
        if (typeof progressId !== "number" || !isRecord(value)) {
          throw new Error("Invalid progress report request");
        }
        const reporter = this.progressReporters.get(progressId);
        if (!reporter) throw new Error("Progress reporter is no longer active");
        const reportValue: { message?: string; increment?: number } = {};
        if (typeof value.message === "string") reportValue.message = value.message;
        if (typeof value.increment === "number" && Number.isFinite(value.increment)) reportValue.increment = value.increment;
        reporter.report(reportValue);
      } else if (method === "window.createOutputChannel") {
        const name = args[0];
        if (typeof name !== "string" || name.trim().length === 0) {
          throw new Error("Invalid output channel name");
        }
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        const channel = api.window.createOutputChannel(name);
        const handleId = this.nextDisposableId++;
        this.outputChannels.set(handleId, channel);
        result = handleId;
      } else if (method.startsWith("window.output.")) {
        const handleId = args[0];
        if (typeof handleId !== "number") {
          throw new Error("Invalid output channel handle");
        }
        const channel = this.outputChannels.get(handleId);
        if (!channel) throw new Error("Unknown output channel handle");
        if (method === "window.output.append") {
          if (typeof args[1] !== "string") throw new Error("Invalid output value");
          channel.append(args[1]);
        } else if (method === "window.output.appendLine") {
          if (typeof args[1] !== "string") throw new Error("Invalid output value");
          channel.appendLine(args[1]);
        } else if (method === "window.output.clear") {
          channel.clear();
        } else if (method === "window.output.show") {
          channel.show(typeof args[1] === "boolean" ? args[1] : false);
        } else if (method === "window.output.hide") {
          channel.hide();
        } else if (method === "window.output.dispose") {
          channel.dispose();
          this.outputChannels.delete(handleId);
        } else {
          throw new Error("Unsupported output channel method: " + method);
        }
      } else if (method === "window.registerTreeDataProvider") {
        if (this.status !== "activating" && this.status !== "active") {
          throw new Error("Late tree provider registration was rejected");
        }
        const viewId = args[0];
        const callbackMap = args[1];
        if (typeof viewId !== "string" || !isCallbackMap(callbackMap)) {
          throw new Error("Invalid tree provider registration");
        }
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        type Provider = Parameters<typeof api.window.registerTreeDataProvider>[1];
        const workerProvider = this.createWorkerProvider(callbackMap);
        const getParent = workerProvider.getParent
          ? this.workerProviderMethod<NonNullable<Provider["getParent"]>>(
              workerProvider,
              "getParent",
            )
          : undefined;
        result = this.storeRemoteDisposable(
          api.window.registerTreeDataProvider(
            viewId,
            {
              getTreeItem: this.workerProviderMethod<Provider["getTreeItem"]>(
                workerProvider,
                "getTreeItem",
              ),
              getChildren: this.workerProviderMethod<Provider["getChildren"]>(
                workerProvider,
                "getChildren",
              ),
              ...(getParent ? { getParent } : {}),
            },
          ),
        );
      } else if (method === "window.registerWebviewViewProvider") {
        if (this.status !== "activating" && this.status !== "active") {
          throw new Error("Late webview provider registration was rejected");
        }
        const viewId = args[0];
        const callbackMap = args[1];
        if (typeof viewId !== "string" || !isCallbackMap(callbackMap)) {
          throw new Error("Invalid webview provider registration");
        }
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        type Provider = Parameters<
          typeof api.window.registerWebviewViewProvider
        >[1];
        const workerProvider = this.createWorkerProvider(callbackMap);
        result = this.storeRemoteDisposable(
          api.window.registerWebviewViewProvider(
            viewId,
            {
              resolveWebviewView: this.workerProviderMethod<
                Provider["resolveWebviewView"]
              >(workerProvider, "resolveWebviewView"),
            },
          ),
        );
      } else if (method === "window.createTerminal") {
        const options = args[0];
        if (!isTerminalOptions(options)) {
          throw new Error("Invalid window.createTerminal request");
        }
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        const terminal = api.window.createTerminal(options);
        const handleId = this.nextDisposableId++;
        this.terminals.set(handleId, terminal);
        result = handleId;
      } else if (method.startsWith("window.terminal.")) {
        const handleId = args[0];
        if (typeof handleId !== "number") {
          throw new Error("Invalid terminal handle");
        }
        const terminal = this.terminals.get(handleId);
        if (!terminal) throw new Error("Unknown terminal handle");
        if (method === "window.terminal.sendText") {
          const text = args[1];
          const addNewLine = args[2];
          if (
            typeof text !== "string" ||
            (addNewLine !== undefined && typeof addNewLine !== "boolean")
          ) {
            throw new Error("Invalid Terminal.sendText request");
          }
          terminal.sendText(text, addNewLine);
        } else if (method === "window.terminal.show") {
          const preserveFocus = args[1];
          if (
            preserveFocus !== undefined &&
            typeof preserveFocus !== "boolean"
          ) {
            throw new Error("Invalid Terminal.show request");
          }
          terminal.show(preserveFocus);
        } else if (method === "window.terminal.hide") {
          terminal.hide();
        } else if (method === "window.terminal.dispose") {
          terminal.dispose();
          this.terminals.delete(handleId);
        } else {
          throw new Error("Unsupported terminal method: " + method);
        }
      } else if (method === "debug.registerConfigurationProvider") {
        if (this.status !== "activating" && this.status !== "active") {
          throw new Error("Late debug provider registration was rejected");
        }
        const type = args[0];
        const callbackMap = args[1];
        if (typeof type !== "string" || !isCallbackMap(callbackMap)) {
          throw new Error("Invalid debug provider registration");
        }
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        type Provider = Parameters<
          typeof api.debug.registerDebugConfigurationProvider
        >[1];
        const workerProvider = this.createWorkerProvider(callbackMap);
        const resolveDebugConfiguration = workerProvider.resolveDebugConfiguration
          ? this.workerProviderMethod<
              NonNullable<Provider["resolveDebugConfiguration"]>
            >(workerProvider, "resolveDebugConfiguration")
          : undefined;
        result = this.storeRemoteDisposable(
          api.debug.registerDebugConfigurationProvider(
            type,
            {
              provideDebugConfigurations: this.workerProviderMethod<
                Provider["provideDebugConfigurations"]
              >(workerProvider, "provideDebugConfigurations"),
              ...(resolveDebugConfiguration ? { resolveDebugConfiguration } : {}),
            },
          ),
        );
      } else if (method === "debug.startDebugging") {
        const config = args[1];
        if (!isRecord(config) || typeof config.type !== "string") {
          throw new Error("Invalid debug.startDebugging request");
        }
        const folder = args[0];
        if (
          folder !== undefined
          && (!isRecord(folder)
            || typeof folder.name !== "string"
            || !isWorkerUri(folder.uri))
        ) {
          throw new Error("Invalid debug workspace folder");
        }
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        result = await api.debug.startDebugging(
          folder as WorkspaceFolder | undefined,
          config as DebugConfiguration,
        );
      } else if (method === "scm.createSourceControl") {
        const sourceControlId = args[0];
        const label = args[1];
        const rootUri = args[2];
        if (
          typeof sourceControlId !== "string"
          || typeof label !== "string"
          || (rootUri !== undefined && !isWorkerUri(rootUri))
        ) {
          throw new Error("Invalid scm.createSourceControl request");
        }
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        const sourceControl = api.scm.createSourceControl(
          sourceControlId,
          label,
          rootUri,
        );
        const handleId = this.nextDisposableId++;
        this.sourceControls.set(handleId, sourceControl);
        result = handleId;
      } else if (method === "scm.setInputBox") {
        const handleId = args[0];
        const value = args[1];
        const placeholder = args[2];
        if (
          typeof handleId !== "number"
          || typeof value !== "string"
          || (placeholder !== undefined && typeof placeholder !== "string")
        ) {
          throw new Error("Invalid SCM input box request");
        }
        const sourceControl = this.sourceControls.get(handleId);
        if (!sourceControl) throw new Error("Unknown source control handle");
        sourceControl.inputBox.value = value;
        sourceControl.inputBox.placeholder = placeholder;
      } else if (method === "scm.createResourceGroup") {
        const handleId = args[0];
        const groupId = args[1];
        const label = args[2];
        if (
          typeof handleId !== "number"
          || typeof groupId !== "string"
          || typeof label !== "string"
        ) {
          throw new Error("Invalid SCM resource group request");
        }
        const sourceControl = this.sourceControls.get(handleId);
        if (!sourceControl) throw new Error("Unknown source control handle");
        const group = sourceControl.createResourceGroup(groupId, label);
        const groupHandleId = this.nextDisposableId++;
        this.sourceControlGroups.set(groupHandleId, {
          group,
          sourceControlHandle: handleId,
        });
        result = groupHandleId;
      } else if (method === "scm.setResourceStates") {
        const handleId = args[0];
        if (typeof handleId !== "number" || !Array.isArray(args[1])) {
          throw new Error("Invalid SCM resource states request");
        }
        const groupRecord = this.sourceControlGroups.get(handleId);
        if (!groupRecord) throw new Error("Unknown source control group handle");
        groupRecord.group.resourceStates = args[1] as SourceControlResourceGroup["resourceStates"];
      } else if (method === "scm.disposeResourceGroup") {
        const handleId = args[0];
        if (typeof handleId !== "number") {
          throw new Error("Invalid source control group handle");
        }
        this.sourceControlGroups.get(handleId)?.group.dispose();
        this.sourceControlGroups.delete(handleId);
      } else if (method === "scm.dispose") {
        const handleId = args[0];
        if (typeof handleId !== "number") {
          throw new Error("Invalid source control handle");
        }
        for (const [groupHandleId, groupRecord] of this.sourceControlGroups) {
          if (groupRecord.sourceControlHandle !== handleId) continue;
          groupRecord.group.dispose();
          this.sourceControlGroups.delete(groupHandleId);
        }
        this.sourceControls.get(handleId)?.dispose();
        this.sourceControls.delete(handleId);
      } else if (method === "env.clipboard.readText") {
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        result = await api.env.clipboard.readText();
      } else if (method === "env.clipboard.writeText") {
        if (typeof args[0] !== "string") {
          throw new Error("Invalid clipboard text");
        }
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        await api.env.clipboard.writeText(args[0]);
      } else if (method === "env.openExternal") {
        if (!isWorkerUri(args[0])) throw new Error("Invalid external URI");
        const api = this.api;
        if (!api) throw new Error("Extension API is unavailable");
        result = await api.env.openExternal(args[0]);
      } else {
        throw new ExtensionApiUnsupportedError(method);
      }
      this.postWorkerRPCResult(id, { result });
    } catch (error) {
      this.postWorkerRPCResult(id, { error: errorMessage(error) });
    }
  }

  private invokeWorkerCallback(
    callbackId: number,
    args: unknown[],
  ): Promise<unknown> {
    if (this.status !== "active") {
      return Promise.reject(
        new Error("Extension command callback is unavailable"),
      );
    }
    const id = this.nextCallbackRequestId++;
    return new Promise((resolve, reject) => {
      this.pendingCallbacks.set(id, { resolve, reject });
      try {
        this.postToWorker({
          type: "invoke-callback",
          id,
          callbackId,
          args: args.map((argument) => toWorkerSerializable(argument)),
        });
      } catch (error) {
        this.pendingCallbacks.delete(id);
        reject(new Error(errorMessage(error)));
      }
    });
  }

  private handleCallbackResult(message: Record<string, unknown>): void {
    if (typeof message.id !== "number") return;
    const pending = this.pendingCallbacks.get(message.id);
    if (!pending) return;
    this.pendingCallbacks.delete(message.id);
    if (typeof message.error === "string") {
      pending.reject(new Error(message.error));
    } else {
      pending.resolve(message.result);
    }
  }

  private handleDeactivated(message: Record<string, unknown>): void {
    if (typeof message.id !== "number") return;
    const pending = this.pendingLifecycle.get(message.id);
    if (!pending) return;
    this.pendingLifecycle.delete(message.id);
    if (typeof message.error === "string") {
      pending.reject(new Error(message.error));
    } else {
      pending.resolve();
    }
  }

  private postWorkerRPCResult(
    id: number,
    payload: { result?: unknown; error?: string },
  ): void {
    if (!this.worker || this.status === "disposed") return;
    try {
      this.postToWorker({
        type: "rpc-result",
        id,
        ...(payload.error !== undefined ? { error: payload.error } : {}),
        ...(payload.error === undefined
          ? { result: toWorkerSerializable(payload.result) }
          : {}),
      });
    } catch {
      // The Worker may terminate while an asynchronous Host RPC is in flight.
    }
  }

  private postToWorker(payload: Record<string, unknown>): void {
    if (!this.worker || this.status === "disposed") {
      throw new Error("Extension Worker is not running");
    }
    this.worker.postMessage({ ...payload, token: this.token });
  }
}

interface ResolvedExtensionRuntime {
  descriptor?: {
    id: string;
    mainPath: string;
    permissions: ExtensionPermission[];
  };
  approveRestricted: boolean;
}

async function ensureSecurityInfosLoaded(
  state: ActivationState,
): Promise<void> {
  if (!state.securityLoadPromise) {
    state.securityLoadPromise = loadExtensionSecurityInfos().catch((error) => {
      state.securityLoadPromise = undefined;
      throw error;
    });
  }
  await state.securityLoadPromise;
}

function normalizeExtensionEntryPath(entry: string): string {
  const normalized = entry.trim().replace(/\\/g, "/").replace(/^\.\/+/, "");
  const segments = normalized.split("/");
  if (
    normalized.length === 0 ||
    normalized.startsWith("/") ||
    /^[A-Za-z]:\//.test(normalized) ||
    segments.some((segment) => segment === "..")
  ) {
    throw new Error("Invalid extension entry path: " + entry);
  }
  return normalized.startsWith("extension/")
    ? normalized
    : "extension/" + normalized;
}

function extensionEntryCandidates(entry: string): string[] {
  const normalized = normalizeExtensionEntryPath(entry);
  if (/\.(?:cjs|mjs|js)$/i.test(normalized)) return [normalized];
  return [
    normalized,
    normalized + ".js",
    normalized + ".cjs",
    normalized + "/index.js",
    normalized + "/index.cjs",
  ];
}

function validateBundledCommonJSSource(
  source: string,
  mainPath: string,
): void {
  if (source.trim().length === 0) {
    throw new Error("Extension main module is empty: " + mainPath);
  }
  if (
    /(?:^|[;{}\r\n])\s*(?:(?:\/\*[\s\S]*?\*\/|\/\/[^\r\n]*)\s*)*(?:import|export)\b/m.test(source) ||
    /\bimport\s*(?:(?:\/\*[\s\S]*?\*\/|\/\/[^\r\n]*)\s*)*\(/.test(source)
  ) {
    throw new Error(
      "Extension main module must be a self-contained CommonJS bundle: " +
        mainPath,
    );
  }
  for (const match of source.matchAll(/\brequire\s*\(\s*(["'])([^"']+)\1\s*\)/g)) {
    if (match[2] !== "vscode") {
      throw new Error(
        "Extension main module requires unsupported CommonJS dependency \"" +
          match[2] +
          "\": " +
          mainPath,
      );
    }
  }
}

async function confirmExtensionRuntimeOperation(
  operation: string,
): Promise<boolean> {
  if (typeof globalThis.confirm !== "function") return false;
  try {
    return globalThis.confirm(
      translate("extensionHost.confirmRuntimeOperation", { operation }),
    );
  } catch {
    return false;
  }
}

async function resolveExtensionRuntime(
  state: ActivationState,
  extId: string,
  manifest: VSCodeExtensionManifest,
): Promise<ResolvedExtensionRuntime> {
  await ensureSecurityInfosLoaded(state);
  const securityInfo = getExtensionSecurityInfo(extId);
  if (!securityInfo) {
    throw new Error("Extension security record is missing for " + extId);
  }
  if (!securityInfo.integrityChecked) {
    throw new Error("Extension SHA-256 integrity check has not passed for " + extId);
  }
  if (securityInfo.blacklisted) {
    throw new Error("Extension is blacklisted: " + extId);
  }
  if (securityInfo.pendingReview) {
    throw new Error("Extension is pending security review: " + extId);
  }
  if (!securityInfo.enabled) {
    throw new Error("Extension is disabled: " + extId);
  }

  const packageSource = decodeExtensionFile(
    await marketplaceService.readExtensionFile(
      manifest.publisher,
      manifest.name,
      EXTENSION_PACKAGE_PATH,
    ),
  );
  let packageJSON: ExtensionPackageJSON;
  try {
    const parsed: unknown = JSON.parse(packageSource);
    if (!isRecord(parsed)) {
      throw new Error("package.json must contain an object");
    }
    packageJSON = parsed;
  } catch (error) {
    throw new Error(
      "Failed to parse extension package.json for " +
        extId +
        ": " +
        errorMessage(error),
    );
  }

  const rawEntries = [
    typeof packageJSON.browser === "string"
      ? packageJSON.browser.trim()
      : "",
    typeof packageJSON.main === "string" ? packageJSON.main.trim() : "",
  ].filter(
    (entry, index, entries) =>
      entry.length > 0 && entries.indexOf(entry) === index,
  );
  if (rawEntries.length === 0) {
    return {
      approveRestricted: securityInfo.level === "restricted",
    };
  }

  const permissions: ExtensionPermission[] = securityInfo.permissions.slice();
  const failures: string[] = [];
  for (const rawEntry of rawEntries) {
    const entryFailures: string[] = [];
    let candidates: string[];
    try {
      candidates = extensionEntryCandidates(rawEntry);
    } catch (error) {
      failures.push(rawEntry + ": " + errorMessage(error));
      continue;
    }
    for (const mainPath of candidates) {
      try {
        const source = decodeExtensionFile(
          await marketplaceService.readExtensionFile(
            manifest.publisher,
            manifest.name,
            mainPath,
          ),
        );
        validateBundledCommonJSSource(source, mainPath);
        state.moduleSourceCache.set(extId, { mainPath, source });
        return {
          descriptor: {
            id: extId,
            mainPath,
            permissions,
          },
          approveRestricted: securityInfo.level === "restricted",
        };
      } catch (error) {
        entryFailures.push(mainPath + ": " + errorMessage(error));
      }
    }
    failures.push(rawEntry + ": " + entryFailures.join("; "));
  }
  throw new Error(
    "No compatible extension entry point for " +
      extId +
      " (" +
      failures.join("; ") +
      ")",
  );
}

// BUG-FIX-2d: 模块级别的回调引用，由主应用在初始化时设置。
// 这样避免在 getExtensionHost 中使用 require() 导致的 Vite 打包问题。
let _onGetActiveTextEditor: (() => TextEditor | undefined) | undefined;
let _onGetWorkspaceFolders: (() => WorkspaceFolder[] | undefined) | undefined;
let _onGetConfiguration: ((section?: string) => Record<string, unknown>) | undefined;
let _onSaveAll: ((
  includeUntitled?: boolean,
) => Promise<{ savedCount: number; failedPaths: string[] }>) | undefined;
let _onNotify: ((level: "info" | "warn" | "error", message: string) => void) | undefined;
let _onShowInputBox: ((options?: InputBoxOptions) => Promise<string | undefined>) | undefined;
let _onShowQuickPick: ((items: string[] | QuickPickItem[], options?: QuickPickOptions) => Promise<string | QuickPickItem | (string | QuickPickItem)[] | undefined>) | undefined;
let _onSetStatusBarMessage: ((text: string, hideAfter?: number) => Disposable) | undefined;
let _onCreateStatusBarItem: (() => import("@/lib/extensionHost/vscodeApi").StatusBarItem) | undefined;
let _onOutput: ((channel: string, action: "append" | "appendLine" | "clear" | "show" | "hide" | "dispose", value?: string) => void) | undefined;
let _onWithProgress: (<R>(options: ProgressOptions, task: (progress: Progress<{ message?: string; increment?: number }>) => Thenable<R>) => Thenable<R>) | undefined;
let _onCreateTextEditorDecorationType: ((extensionId: string, options: DecorationRenderOptions) => TextEditorDecorationType) | undefined;

/**
 * BUG-FIX-2d: 设置活跃编辑器状态回调。
 * 由主 Vue 应用在初始化扩展系统时调用。
 */
export function setExtensionHostActiveEditorCallback(
  cb: () => TextEditor | undefined,
): void {
  _onGetActiveTextEditor = cb;
}

/** Set the current single-root workspace folder for extension APIs. */
export function setExtensionHostWorkspaceFoldersCallback(
  cb: () => WorkspaceFolder[] | undefined,
): void {
  _onGetWorkspaceFolders = cb;
}

/**
 * BUG-FIX-2d: 设置配置读取回调。
 * 由主 Vue 应用在初始化扩展系统时调用。
 */
export function setExtensionHostConfigurationCallback(
  cb: (section?: string) => Record<string, unknown>,
): void {
  _onGetConfiguration = cb;
}

export function notifyExtensionHostConfigurationChange(section?: string): void {
  activationState?.extensionHost?.notifyConfigurationChange(section);
}

/** Forward one real Monaco selection update to subscribed VSIX Workers. */
export function notifyExtensionHostTextEditorSelectionChange(
  path: string,
  selections: readonly import("@/lib/extensionHost/vscodeApi").Selection[],
): void {
  const host = activationState?.extensionHost;
  const textEditor = _onGetActiveTextEditor?.();
  if (!host || !textEditor || selections.length === 0) return;
  const normalize = (value: string) => value.replaceAll("\\", "/").toLowerCase();
  if (normalize(textEditor.document.uri.fsPath) !== normalize(path)) return;
  host.notifyTextEditorSelectionChange({ textEditor, selections });
}

/**
 * G13: 设置 saveAll 回调（真实保存 dirty buffers 并传播逐文件失败）。
 * 由主 Vue 应用注入 editor store 的 saveAllFilesDetailed。
 */
export function setExtensionHostSaveAllCallback(
  cb: (
    includeUntitled?: boolean,
  ) => Promise<{ savedCount: number; failedPaths: string[] }>,
): void {
  _onSaveAll = cb;
}

/**
 * G13: 设置通知回调（真实 UI 通知）。由主 Vue 应用注入 lib/notifications。
 */
export function setExtensionHostNotifyCallback(
  cb: (level: "info" | "warn" | "error", message: string) => void,
): void {
  _onNotify = cb;
}

export function setExtensionHostInputCallback(
  cb: ((options?: InputBoxOptions) => Promise<string | undefined>) | undefined,
): void { _onShowInputBox = cb; }

export function setExtensionHostQuickPickCallback(
  cb: ((items: string[] | QuickPickItem[], options?: QuickPickOptions) => Promise<string | QuickPickItem | (string | QuickPickItem)[] | undefined>) | undefined,
): void { _onShowQuickPick = cb; }

export function setExtensionHostStatusBarCallback(
  message: ((text: string, hideAfter?: number) => Disposable) | undefined,
  item: (() => import("@/lib/extensionHost/vscodeApi").StatusBarItem) | undefined,
): void { _onSetStatusBarMessage = message; _onCreateStatusBarItem = item; }

export function setExtensionHostOutputCallback(
  cb: ((channel: string, action: "append" | "appendLine" | "clear" | "show" | "hide" | "dispose", value?: string) => void) | undefined,
): void { _onOutput = cb; }

export function setExtensionHostProgressCallback(
  cb: <R>(options: ProgressOptions, task: (progress: Progress<{ message?: string; increment?: number }>) => Thenable<R>) => Thenable<R>,
): void { _onWithProgress = cb; }

/** Set the real editor decoration factory used by VSIX Workers. */
export function setExtensionHostDecorationCallback(
  cb: ((extensionId: string, options: DecorationRenderOptions) => TextEditorDecorationType) | undefined,
): void {
  _onCreateTextEditorDecorationType = cb;
}

function getExtensionHost(state: ActivationState): ExtensionHost {
  if (!state.extensionHost) {
    state.extensionHost = new ExtensionHost({
      loadModule: (extensionId, mainPath) =>
        loadExtensionWorkerModule(state, extensionId, mainPath),
      monaco: monaco as unknown as MonacoBridge,
      confirmHandler: (operation) =>
        confirmExtensionRuntimeOperation(operation),
      // BUG-FIX-2d: 桥接活跃编辑器状态到扩展宿主。
      onGetActiveTextEditor: () => _onGetActiveTextEditor?.(),
      onGetWorkspaceFolders: () => _onGetWorkspaceFolders?.(),
      // BUG-FIX-2d: 桥接设置到扩展宿主。
      onGetConfiguration: (section) => _onGetConfiguration?.(section) ?? {},
      // G13: 真实 saveAll（editor store）与真实通知（lib/notifications）。
      onSaveAll: (includeUntitled) => _onSaveAll?.(includeUntitled) ?? Promise.reject(
        new Error("extension host saveAll callback is not wired"),
      ),
      onNotify: (level, message) => _onNotify?.(level, message),
      onShowInputBox: (options) => _onShowInputBox?.(options) ?? Promise.reject(
        new ExtensionApiUnsupportedError("window.showInputBox", "host input UI callback is not wired"),
      ),
      onShowQuickPick: (items, options) => _onShowQuickPick?.(items, options) ?? Promise.reject(
        new ExtensionApiUnsupportedError("window.showQuickPick", "host quick-pick UI callback is not wired"),
      ),
      onSetStatusBarMessage: (text, hideAfter) => _onSetStatusBarMessage?.(text, hideAfter) ?? (() => {
        throw new ExtensionApiUnsupportedError("window.setStatusBarMessage", "host status bar callback is not wired");
      })(),
      onCreateStatusBarItem: () => _onCreateStatusBarItem?.() ?? (() => {
        throw new ExtensionApiUnsupportedError("window.createStatusBarItem", "host status bar callback is not wired");
      })(),
      onOutput: (channel, action, value) => {
        if (!_onOutput) throw new ExtensionApiUnsupportedError("window.createOutputChannel", "host output callback is not wired");
        _onOutput(channel, action, value);
      },
      onWithProgress: (options, task) => _onWithProgress?.(options, task) ?? Promise.reject(
        new ExtensionApiUnsupportedError("window.withProgress", "host progress UI callback is not wired"),
      ),
      onCreateTextEditorDecorationType: (extensionId, options) => _onCreateTextEditorDecorationType?.(extensionId, options) ?? (() => {
        throw new ExtensionApiUnsupportedError("window.createTextEditorDecorationType", "host decoration callback is not wired");
      })(),
    });
  }
  return state.extensionHost;
}

async function loadExtensionWorkerModule(
  state: ActivationState,
  extensionId: string,
  mainPath: string,
): Promise<ExtensionModule> {
  if (state.resetting || activationState !== state) {
    throw new Error("Extension activation state is no longer current");
  }
  const manifest = state.manifestCache.get(extensionId);
  if (!manifest) {
    throw new Error("Extension manifest not found for " + extensionId);
  }
  const cached = state.moduleSourceCache.get(extensionId);
  const source =
    cached?.mainPath === mainPath
      ? cached.source
      : decodeExtensionFile(
          await marketplaceService.readExtensionFile(
            manifest.publisher,
            manifest.name,
            mainPath,
          ),
        );
  if (state.resetting || activationState !== state) {
    throw new Error("Extension activation state was reset while loading");
  }
  validateBundledCommonJSSource(source, mainPath);

  const workerModule = new WorkerExtensionModule(
    extensionId,
    mainPath,
    source,
    () => state.workerModules.delete(workerModule),
    (error) => {
      if (!state.resetting && activationState === state) {
        console.warn(
          "[F-3] extension Worker crashed for " + extensionId + ":",
          error,
        );
        scheduleExtensionWorkerRecovery(state, extensionId, error);
      }
    },
  );
  state.workerModules.add(workerModule);
  return workerModule;
}

function scheduleExtensionWorkerRecovery(
  state: ActivationState,
  extensionId: string,
  error: Error,
): void {
  if (state.lifecycleHolds.has(extensionId) || state.workerRecoveryPromises.has(extensionId)) return;
  const generation = state.workerRecoveryGeneration.get(extensionId) ?? 0;
  const recovery = (async () => {
    await deactivateExtensionInState(state, extensionId);
    if (
      state.resetting ||
      activationState !== state ||
      (state.workerRecoveryGeneration.get(extensionId) ?? 0) !== generation
    ) {
      return;
    }

    let attempts = state.workerRestartAttempts.get(extensionId) ?? 0;
    while (attempts < MAX_EXTENSION_WORKER_RESTARTS) {
      attempts += 1;
      state.workerRestartAttempts.set(extensionId, attempts);
      const restored = await activateExtension(state, extensionId);
      if (restored) {
        // A successful activation closes the failure streak. Keep the hard
        // limit for consecutive failed attempts without disabling an
        // extension after unrelated, later runtime faults.
        state.workerRestartAttempts.delete(extensionId);
        return;
      }
      if (
        state.resetting ||
        activationState !== state ||
        (state.workerRecoveryGeneration.get(extensionId) ?? 0) !== generation
      ) {
        return;
      }
      console.warn(
        `[F-3] extension Worker recovery failed for ${extensionId} ` +
          `(attempt ${attempts}/${MAX_EXTENSION_WORKER_RESTARTS})`,
      );
    }
    console.warn(
      `[F-3] extension Worker restart limit reached for ${extensionId}:`,
      error,
    );
  })().finally(() => {
    if (state.workerRecoveryPromises.get(extensionId) === recovery) {
      state.workerRecoveryPromises.delete(extensionId);
    }
  });
  state.workerRecoveryPromises.set(extensionId, recovery);
  void recovery.catch((recoveryError) => {
    console.warn(
      `[F-3] failed to recover crashed extension ${extensionId}:`,
      recoveryError,
    );
  });
}

// ---------------------------------------------------------------------------
// Manifest 加载
// ---------------------------------------------------------------------------

/**
 * F-3: 加载所有已安装扩展的 manifest 到 manifestCache，并预注册惰性命令
 * descriptor。在 App.vue 启动时调用一次。重复调用幂等（除非先调用
 * resetActivationState）。
 */
export async function loadInstalledExtensionManifests(): Promise<void> {
  await waitForPriorExtensionHostTeardown();
  const state = getActivationState();
  if (state.manifestsLoaded) return;
  try {
    const manifests = await marketplaceService.getInstalledExtensionManifests();
    // A reset may occur while the backend request is in flight (for example
    // during HMR). Never repopulate a state container that has been detached.
    if (activationState !== state) return;
    state.manifestCache.clear();
    for (const m of manifests) {
      const id = extensionId(m);
      if (id) {
        state.manifestCache.set(id, m);
        injectCommands(state, id, m.parsedContributes?.commands);
      }
    }
    state.manifestsLoaded = true;
  } catch (err) {
    // 后端调用失败不阻断启动——扩展功能降级为不可用。
    console.warn("[F-3] loadInstalledExtensionManifests failed:", err);
  }
}

/**
 * F-3: 重置激活状态（测试用）。清空缓存与已激活集合，并从 vscodeExtensions
 * registry 移除所有已注入的 commands/views。
 */
export async function resetActivationState(): Promise<void> {
  const state = activationState;
  if (!state) {
    await waitForPriorExtensionHostTeardown();
    return;
  }

  const teardown = performActivationStateReset(state);
  activationRuntimeGlobal.__koyoriIdeExtensionHostTeardown = teardown;
  try {
    await teardown;
  } finally {
    if (activationRuntimeGlobal.__koyoriIdeExtensionHostTeardown === teardown) {
      delete activationRuntimeGlobal.__koyoriIdeExtensionHostTeardown;
    }
  }
}

import.meta.hot?.dispose(() => {
  void resetActivationState();
});

async function performActivationStateReset(
  state: ActivationState,
): Promise<void> {
  activationState = undefined;
  state.resetting = true;
  for (const [extId, manifest] of state.manifestCache) {
    cleanupInjectedExtension(state, extId, manifest);
  }
  state.injectedExtensions.clear();
  state.activatedExtensions.clear();

  const resetError = new Error("Extension activation state was reset");
  const hostDisposal = state.extensionHost?.disposeAll() ?? Promise.resolve();
  const pendingWork = Promise.allSettled([
    ...state.activationPromises.values(),
    ...state.deactivationPromises.values(),
    ...state.workerRecoveryPromises.values(),
    hostDisposal,
  ]);

  if (!await waitForActivationResetGrace(pendingWork)) {
    for (const module of Array.from(state.workerModules)) {
      module.terminate(resetError);
    }
    await waitForActivationResetGrace(pendingWork);
  }
  await resetExtensionHostModuleState();

  for (const module of Array.from(state.workerModules)) {
    module.terminate(resetError);
  }

  state.manifestCache.clear();
  state.activationErrors.clear();
  state.activationPromises.clear();
  state.deactivationPromises.clear();
  state.deactivationRequested.clear();
  state.moduleSourceCache.clear();
  state.workerModules.clear();
  state.workerRestartAttempts.clear();
  state.workerRecoveryPromises.clear();
  state.workerRecoveryGeneration.clear();
  state.lifecycleHolds.clear();
  state.manifestsLoaded = false;
}

/**
 * F-3: 获取 manifest 缓存（测试或调试用）。
 */
export function getManifestCache(): ReadonlyMap<string, VSCodeExtensionManifest> {
  return getActivationState().manifestCache;
}

/**
 * Replace the cached manifest and security record for one installed extension.
 * Callers must stop the old runtime before using this after an update.
 */
export async function refreshExtensionCaches(
  publisher: string,
  name: string,
): Promise<void> {
  await waitForPriorExtensionHostTeardown();
  const state = getActivationState();
  const extId = `${publisher}.${name}`;
  // The backend update has already committed. Drop every reference to the old
  // package before fetching replacement metadata so a refresh failure cannot
  // make stale code activatable again.
  invalidateExtensionCaches(extId);
  const [manifest] = await Promise.all([
    marketplaceService.getExtensionManifest(publisher, name),
    refreshExtensionSecurityInfo(extId),
  ]);
  if (activationState !== state || state.resetting) return;
  if (extensionId(manifest) !== extId) {
    throw new Error(`Updated extension manifest identity mismatch for ${extId}`);
  }
  state.manifestCache.set(extId, manifest);
  injectCommands(state, extId, manifest.parsedContributes?.commands);
}

/** Remove only one extension's renderer-side manifest, module, and security state. */
export function invalidateExtensionCaches(extId: string): void {
  const state = activationState;
  if (state) {
    invalidateExtensionRuntimeCaches(state, extId);
  }
  removeExtensionSecurityInfo(extId);
}

function invalidateExtensionRuntimeCaches(
  state: ActivationState,
  extId: string,
): void {
  const manifest = state.manifestCache.get(extId);
  cleanupInjectedExtension(state, extId, manifest);
  state.manifestCache.delete(extId);
  state.moduleSourceCache.delete(extId);
  state.activatedExtensions.delete(extId);
  state.activationErrors.delete(extId);
  state.workerRestartAttempts.delete(extId);
  state.workerRecoveryGeneration.set(
    extId,
    (state.workerRecoveryGeneration.get(extId) ?? 0) + 1,
  );
}

// ---------------------------------------------------------------------------
// 激活触发
// ---------------------------------------------------------------------------

/**
 * F-3: 触发 onLanguage:<language> 激活。在 editor.ts:openFile 打开文件时调用。
 * 返回本次新激活的扩展 ID 列表。
 */
export async function activateOnLanguage(language: string): Promise<string[]> {
  await loadInstalledExtensionManifests();
  const extIds = await safeTrigger(() =>
    marketplaceService.triggerActivationOnLanguage(language),
  );
  await activateExtensions(extIds);
  return extIds;
}

/**
 * F-3: 触发 onCommand:<command> 激活。在执行 VS Code 扩展命令前调用。
 */
export async function activateOnCommand(command: string): Promise<string[]> {
  await loadInstalledExtensionManifests();
  const extIds = await safeTrigger(() =>
    marketplaceService.triggerActivationOnCommand(command),
  );
  await activateExtensions(extIds);
  return extIds;
}

/**
 * F-3: 触发 workspaceContains:<glob> 激活。在 app.ts:openProject 打开工作区时调用。
 */
export async function activateOnWorkspaceContains(workspaceRoot: string): Promise<string[]> {
  await loadInstalledExtensionManifests();
  const extIds = await safeTrigger(() =>
    marketplaceService.triggerActivationWorkspaceContains(workspaceRoot),
  );
  await activateExtensions(extIds);
  return extIds;
}

/**
 * F-3: 触发 onDebug 激活。在 debugService 启动调试会话时调用。
 */
export async function activateOnDebug(): Promise<string[]> {
  await loadInstalledExtensionManifests();
  const extIds = await safeTrigger(() => marketplaceService.triggerActivationOnDebug());
  await activateExtensions(extIds);
  return extIds;
}

/**
 * F-3: 触发 onDebugResolve:<type> 激活。
 */
export async function activateOnDebugResolve(debugType: string): Promise<string[]> {
  await loadInstalledExtensionManifests();
  const extIds = await safeTrigger(() =>
    marketplaceService.triggerActivationOnDebugResolve(debugType),
  );
  await activateExtensions(extIds);
  return extIds;
}

/**
 * F-3: 触发 "*" eager 激活。在 App.vue 启动时调用。注意：声明 "*" 的扩展
 * 会在启动时激活，可能影响性能。调用方应提示用户确认是否启用这些扩展。
 */
export async function activateEager(): Promise<string[]> {
  await loadInstalledExtensionManifests();
  const extIds = await safeTrigger(() => marketplaceService.triggerActivationEager());
  await activateExtensions(extIds);
  return extIds;
}

/**
 * F-3: 查询扩展是否已激活。
 */
export function isExtensionActivated(extensionId: string): boolean {
  return getActivationState().activatedExtensions.has(extensionId);
}

/** Prevent activation and Worker crash recovery during a backend transaction. */
export function beginExtensionLifecycleHold(extensionId: string): void {
  const state = getActivationState();
  state.lifecycleHolds.add(extensionId);
  state.workerRestartAttempts.delete(extensionId);
  state.workerRecoveryGeneration.set(
    extensionId,
    (state.workerRecoveryGeneration.get(extensionId) ?? 0) + 1,
  );
}

/** Release a transaction hold. Releasing never implicitly reactivates a Worker. */
export function releaseExtensionLifecycleHold(extensionId: string): void {
  activationState?.lifecycleHolds.delete(extensionId);
}

export function isExtensionLifecycleHeld(extensionId: string): boolean {
  return activationState?.lifecycleHolds.has(extensionId) ?? false;
}

/** Reactivate a retained manifest after an update transaction rolls back. */
export async function reactivateExtension(extensionId: string): Promise<boolean> {
  if (isExtensionLifecycleHeld(extensionId)) return false;
  await activateExtensions([extensionId]);
  return getActivationState().activatedExtensions.has(extensionId);
}

/** Restore lazy command descriptors retained after a failed replacement. */
export function restoreExtensionContributions(extensionId: string): void {
  const state = activationState;
  const manifest = state?.manifestCache.get(extensionId);
  if (!state || !manifest || state.resetting) return;
  injectCommands(state, extensionId, manifest.parsedContributes?.commands);
}

// ---------------------------------------------------------------------------
// 激活执行：注入 contributes
// ---------------------------------------------------------------------------

/**
 * F-3: 激活一组扩展。对每个扩展：
 *   1. 从 manifestCache 取 manifest
 *   2. 注入 contributes.commands 到 vscodeExtensions.ts 命令 registry
 *   3. 注入 contributes.views 到 vscodeExtensions.ts 视图 registry
 *   4. 注入 contributes.grammars 到 vscodeExtensions.ts grammar registry
 *   5. 注入 contributes.snippets 到 vscodeExtensions.ts snippet registry
 *   6. 同步 grammars/snippets 到 Monaco（语言注册 + snippet completion provider）
 *   7. 通过 ExtensionHost 在专用 Worker 中加载主模块
 *   8. 主模块激活成功后标记为已激活
 */
async function activateExtensions(extIds: string[]): Promise<void> {
  await waitForPriorExtensionHostTeardown();
  await loadInstalledExtensionManifests();
  const state = getActivationState();
  let hasNewGrammarsOrSnippets = false;
  for (const extId of extIds) {
    const activated = await activateExtension(state, extId);
    const activationSucceeded = activated || state.activatedExtensions.has(extId);
    try {
      await marketplaceService.reportExtensionActivation(extId, activationSucceeded);
    } catch (error) {
      console.warn(`[F-3] failed to report activation result for ${extId}:`, error);
    }
    const manifest = state.manifestCache.get(extId);
    if (
      activated &&
      (manifest?.parsedContributes?.grammars?.length ||
        manifest?.parsedContributes?.snippets?.length)
    ) {
      hasNewGrammarsOrSnippets = true;
    }
  }
  // 如果有新 grammars 或 snippets，同步到 Monaco。
  // 使用动态 import 避免循环依赖（monacoExtensionContributes 导入 vscodeExtensions，
  // 而本模块也导入 vscodeExtensions；动态 import 打断静态依赖链）。
  if (hasNewGrammarsOrSnippets) {
    try {
      const { syncExtensionGrammarsToMonaco, syncExtensionSnippetsToMonaco } =
        await import("@/lib/monacoExtensionContributes");
      syncExtensionGrammarsToMonaco();
      // snippet 加载是异步的（需读取文件），不阻塞激活流程。
      void syncExtensionSnippetsToMonaco().catch((error: unknown) => {
        console.warn("[F-3] Monaco snippets sync failed:", error);
      });
    } catch (err) {
      console.warn("[F-3] Monaco contributes sync failed:", err);
    }
  }
}

async function activateExtension(
  state: ActivationState,
  extId: string,
): Promise<boolean> {
  if (state.lifecycleHolds.has(extId)) return false;
  if (state.activatedExtensions.has(extId)) return false;
  const deactivation = state.deactivationPromises.get(extId);
  if (deactivation) {
    await deactivation;
  }
  if (
    state.resetting ||
    activationState !== state ||
    state.deactivationRequested.has(extId)
  ) {
    return false;
  }

  const existing = state.activationPromises.get(extId);
  if (existing) return existing;
  const activation = performExtensionActivation(state, extId).finally(() => {
    if (state.activationPromises.get(extId) === activation) {
      state.activationPromises.delete(extId);
    }
  });
  state.activationPromises.set(extId, activation);
  return activation;
}

async function performExtensionActivation(
  state: ActivationState,
  extId: string,
): Promise<boolean> {
  const manifest = state.manifestCache.get(extId);
  if (!manifest) return false;

  let host: ExtensionHost | undefined;
  try {
    const runtime = await resolveExtensionRuntime(state, extId, manifest);
    if (
      state.resetting ||
      activationState !== state ||
      state.deactivationRequested.has(extId) ||
      state.lifecycleHolds.has(extId)
    ) {
      return false;
    }

    injectContributes(state, extId, manifest.parsedContributes);
    state.injectedExtensions.add(extId);

    if (runtime.descriptor) {
      host = getExtensionHost(state);
      if (runtime.approveRestricted) {
        host.approveExtension(extId);
      }
      await host.activate(runtime.descriptor);
    }

    if (
      state.resetting ||
      activationState !== state ||
      state.deactivationRequested.has(extId) ||
      state.lifecycleHolds.has(extId)
    ) {
      if (host) await host.deactivate(extId);
      cleanupInjectedExtension(state, extId, manifest);
      return false;
    }
    state.activatedExtensions.add(extId);
    state.activationErrors.delete(extId);
    return true;
  } catch (error) {
    const activationError =
      error instanceof Error ? error : new Error(String(error));
    state.activationErrors.set(extId, activationError);
    if (host) await host.deactivate(extId);
    cleanupInjectedExtension(state, extId, manifest);
    console.warn(
      "[F-3] extension activation failed for " + extId + ":",
      error,
    );
    return false;
  }
}

/**
 * F-3: 把扩展的 contributes 注入到前端 registry。
 *   - commands  → registerVscodeExtensionCommand（命令面板可见）
 *   - views     → registerVscodeExtensionViews（侧边栏可见）
 *   - grammars  → registerVscodeExtensionGrammars（Monaco 语言配置）
 *   - snippets  → registerVscodeExtensionSnippets（Monaco snippet 注册表）
 *
 * 命令 handler 只转发给当前 ActivationState 所拥有的 ExtensionHost。
 */
function injectContributes(
  state: ActivationState,
  extId: string,
  contributes: ExtensionContributes | undefined,
): void {
  if (!contributes) return;

  // 注入 commands。
  injectCommands(state, extId, contributes.commands);

  // 注入 views：按容器 ID 分组注册。
  if (contributes.views) {
    for (const [container, viewList] of Object.entries(contributes.views)) {
      if (!viewList || viewList.length === 0) continue;
      const validViews = viewList.filter((v) => v.id);
      if (validViews.length > 0) {
        registerVscodeExtensionViews(extId, container, validViews);
      }
    }
  }

  // 注入 grammars：按 language ID 分组注册。
  // grammar 的 language 字段可选（注入式 grammar 无 language），缺失时归入 "" key。
  if (contributes.grammars) {
    const byLanguage = new Map<string, ExtensionGrammarContribution[]>();
    for (const grammar of contributes.grammars) {
      if (!grammar.scopeName) continue;
      const lang = grammar.language ?? "";
      const list = byLanguage.get(lang) ?? [];
      list.push(grammar);
      byLanguage.set(lang, list);
    }
    for (const [lang, list] of byLanguage.entries()) {
      registerVscodeExtensionGrammars(extId, lang, list);
    }
  }

  // 注入 snippets：按 language ID 分组注册。
  if (contributes.snippets) {
    const byLanguage = new Map<string, ExtensionSnippetContribution[]>();
    for (const snippet of contributes.snippets) {
      if (!snippet.language || !snippet.path) continue;
      const list = byLanguage.get(snippet.language) ?? [];
      list.push(snippet);
      byLanguage.set(snippet.language, list);
    }
    for (const [lang, list] of byLanguage.entries()) {
      registerVscodeExtensionSnippets(extId, lang, list);
    }
  }

  if (contributes.themes) {
    registerVscodeExtensionThemes(
      extId,
      contributes.themes.filter((theme) => Boolean(theme.label && theme.path)),
    );
  }
}

function injectCommands(
  state: ActivationState,
  extId: string,
  commands: ExtensionCommandContribution[] | undefined,
): void {
  if (!commands) return;
  for (const cmd of commands) {
    if (!cmd.command) continue;
    try {
      registerVscodeExtensionCommand({
        id: cmd.command,
        extensionId: extId,
        label: cmd.title || cmd.command,
        category: cmd.category,
        handler: createCommandHandler(state, extId, cmd),
      });
    } catch (err) {
      // 命令冲突（已被其他扩展注册）——跳过并记录。
      console.warn(`[F-3] command "${cmd.command}" registration skipped:`, err);
    }
  }
}

/**
 * F-3: 创建 VS Code 扩展命令的 handler。静态贡献只负责展示，真正执行
 * 委托给扩展在 activate() 中注册到 ExtensionHost 的命令回调。
 */
function createCommandHandler(
  ownerState: ActivationState,
  extId: string,
  cmd: ExtensionCommandContribution,
): (...args: unknown[]) => Promise<unknown> {
  return async (...args: unknown[]) => {
    if (activationState !== ownerState || ownerState.resetting) {
      throw new Error("Extension activation state is unavailable: " + extId);
    }
    if (!ownerState.activatedExtensions.has(extId)) {
      await activateOnCommand(cmd.command);
    }
    // Recent VS Code manifests infer onCommand activation events. Keep those
    // commands executable while still using the shared activation/report path.
    if (!ownerState.activatedExtensions.has(extId)) {
      await activateExtensions([extId]);
    }
    if (!ownerState.activatedExtensions.has(extId)) {
      const cause = ownerState.activationErrors.get(extId);
      throw new Error(
        "Extension activation failed: " +
          extId +
          (cause ? ": " + cause.message : ""),
        cause ? { cause } : undefined,
      );
    }
    const host = ownerState.extensionHost;
    if (!host?.isActive(extId)) {
      throw new Error(
        "Extension has no executable command handler: " + extId,
      );
    }
    return host.executeCommand(cmd.command, ...args);
  };
}

// ---------------------------------------------------------------------------
// 卸载/停用
// ---------------------------------------------------------------------------

/**
 * F-3: 停用一个扩展。从 registry 移除其 commands/views/grammars/snippets，
 * 并标记为未激活。调用方为扩展卸载/禁用路径。
 */
export function deactivateExtension(extId: string): Promise<void> {
  const state = activationState;
  if (!state) return Promise.resolve();
  state.workerRestartAttempts.delete(extId);
  state.workerRecoveryGeneration.set(
    extId,
    (state.workerRecoveryGeneration.get(extId) ?? 0) + 1,
  );
  return deactivateExtensionInState(state, extId);
}

function deactivateExtensionInState(
  state: ActivationState,
  extId: string,
): Promise<void> {
  const existing = state.deactivationPromises.get(extId);
  if (existing) return existing;

  state.deactivationRequested.add(extId);
  const manifest = state.manifestCache.get(extId);
  const shouldResyncGrammars = Boolean(
    manifest?.parsedContributes?.grammars?.length,
  );
  const shouldResyncSnippets = Boolean(
    manifest?.parsedContributes?.snippets?.length,
  );
  cleanupInjectedExtension(state, extId, manifest);
  state.activatedExtensions.delete(extId);

  const deactivation = (async () => {
    try {
      const activation = state.activationPromises.get(extId);
      if (activation) await activation;
      await state.extensionHost?.deactivate(extId);
    } finally {
      state.deactivationRequested.delete(extId);
      try {
        await marketplaceService.reportExtensionDeactivated(extId);
      } catch (error) {
        console.warn(`[F-3] failed to report deactivation for ${extId}:`, error);
      }
      scheduleMonacoContributesCleanup(
        extId,
        shouldResyncGrammars,
        shouldResyncSnippets,
      );
    }
  })().finally(() => {
    if (state.deactivationPromises.get(extId) === deactivation) {
      state.deactivationPromises.delete(extId);
    }
  });
  state.deactivationPromises.set(extId, deactivation);
  return deactivation;
}

function cleanupInjectedExtension(
  state: ActivationState,
  extId: string,
  manifest: VSCodeExtensionManifest | undefined,
): void {
  if (!state.injectedExtensions.delete(extId)) {
    cleanupExtensionCommands(extId, manifest?.parsedContributes?.commands);
    return;
  }
  cleanupExtensionContributions(extId, manifest);
}

function scheduleMonacoContributesCleanup(
  extId: string,
  shouldResyncGrammars: boolean,
  shouldResyncSnippets: boolean,
): void {
  if (!shouldResyncGrammars && !shouldResyncSnippets) return;
  void import("@/lib/monacoExtensionContributes")
    .then(async ({ syncExtensionGrammarsToMonaco, syncExtensionSnippetsToMonaco }) => {
      if (shouldResyncGrammars) syncExtensionGrammarsToMonaco();
      if (shouldResyncSnippets) await syncExtensionSnippetsToMonaco();
    })
    .catch((err: unknown) => {
      console.warn(
        `[F-3] Monaco contributes cleanup failed for ${extId}:`,
        err,
      );
    });
}

/** Release every registry contribution injected for one manifest. */
function cleanupExtensionContributions(
  extId: string,
  manifest: VSCodeExtensionManifest | undefined,
): void {
  const contributes = manifest?.parsedContributes;
  cleanupExtensionCommands(extId, contributes?.commands);
  if (contributes?.views) {
    for (const [container, viewList] of Object.entries(contributes.views)) {
      const viewIds = viewList.map((v) => v.id).filter((id): id is string => Boolean(id));
      if (viewIds.length > 0) {
        unregisterVscodeExtensionViews(extId, container, viewIds);
      }
    }
  }
  if (contributes?.grammars) {
    // 按 language 分组注销 grammar（按 scopeName 标识）。
    const byLanguage = new Map<string, string[]>();
    for (const grammar of contributes.grammars) {
      if (!grammar.scopeName) continue;
      const lang = grammar.language ?? "";
      const list = byLanguage.get(lang) ?? [];
      list.push(grammar.scopeName);
      byLanguage.set(lang, list);
    }
    for (const [lang, scopeNames] of byLanguage.entries()) {
      unregisterVscodeExtensionGrammars(extId, lang, scopeNames);
    }
  }
  if (contributes?.snippets) {
    // 按 language 分组注销 snippet（按 path 标识）。
    const byLanguage = new Map<string, string[]>();
    for (const snippet of contributes.snippets) {
      if (!snippet.language || !snippet.path) continue;
      const list = byLanguage.get(snippet.language) ?? [];
      list.push(snippet.path);
      byLanguage.set(snippet.language, list);
    }
    for (const [lang, paths] of byLanguage.entries()) {
      unregisterVscodeExtensionSnippets(extId, lang, paths);
    }
  }
  if (contributes?.themes) {
    const removedActiveTheme = getActiveVscodeExtensionTheme()?.extensionId === extId;
    unregisterVscodeExtensionThemes(
      extId,
      contributes.themes
        .map((theme) => theme.path)
        .filter((path): path is string => Boolean(path)),
    );
    if (removedActiveTheme) restoreBuiltInMonacoTheme();
  }
}

function cleanupExtensionCommands(
  extId: string,
  commands: ExtensionCommandContribution[] | undefined,
): void {
  if (!commands) return;
  const ownedCommandIds = new Set(
    listVscodeExtensionCommands()
      .filter((command) => command.extensionId === extId)
      .map((command) => command.id),
  );
  for (const command of commands) {
    if (command.command && ownedCommandIds.has(command.command)) {
      unregisterVscodeExtensionCommand(command.command);
    }
  }
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** 构造扩展 ID："publisher.name"。 */
function extensionId(m: VSCodeExtensionManifest): string {
  if (!m.publisher || !m.name) return "";
  return `${m.publisher}.${m.name}`;
}

/**
 * 安全调用后端 trigger 方法。捕获错误并返回空数组，避免后端调用失败
 * 阻断前端流程（如编辑器打开文件）。
 */
async function safeTrigger(fn: () => Promise<string[]>): Promise<string[]> {
  try {
    return await fn();
  } catch (err) {
    console.warn("[F-3] triggerActivation failed:", err);
    return [];
  }
}

/**
 * F-3: 获取所有已激活的扩展 ID（测试/调试用）。
 */
export function getActivatedExtensions(): string[] {
  return Array.from(getActivationState().activatedExtensions);
}

/** Return the last fail-closed activation diagnostic for one extension. */
export function getExtensionActivationError(extensionId: string): Error | undefined {
  return getActivationState().activationErrors.get(extensionId);
}

/**
 * F-3: 获取指定容器的视图列表（转发到 vscodeExtensions.ts）。
 * 提供此便捷方法供 SidePanel / ActivityBar 使用。
 */
export function getViewsForContainer(container: string): ExtensionViewContribution[] {
  return listVscodeExtensionViews(container);
}
