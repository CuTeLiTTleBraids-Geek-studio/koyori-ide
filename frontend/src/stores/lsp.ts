// Koyori IDE 模块 · Lsp；交互服务：离线 LSP（LSPService）。
// 喵，这是 Koyori IDE 的 Lsp 模块（前端实现）~
import { reactive, computed, ref } from "vue";
import * as defaultMonaco from "monaco-editor";
import type * as monacoEditor from "monaco-editor";
import { Events } from "@wailsio/runtime";
import { lspService } from "@/api/services";
import { appState } from "@/stores/app";
import { pushOutput } from "@/stores/output";
import * as LSPServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/lspservice.js";
import type {
  LSPServerStatus,
  LSPCompletionRequest,
  LSPCompletionItem,
  LSPCompletionList,
  LSPLocation,
  LSPTextEdit,
  LSPRange,
  LSPDocumentSymbol,
  LSPSymbolInformation,
  SemanticToken,
  InlayHint,
  InlayHintLabelPart,
  LSPDocumentLink,
  LSPSelectionRange,
  LSPFoldingRange,
  LSPCallHierarchyItem,
  LSPCallHierarchyIncomingCall,
  LSPCallHierarchyOutgoingCall,
  LSPTypeHierarchyItem,
  WorkspaceSymbol,
  Diagnostic,
} from "@/types";

type MonacoApi = typeof import("monaco-editor");

interface PullDiagnosticsModelState {
  contentDisposable: monacoEditor.IDisposable | null;
  timer: ReturnType<typeof setTimeout> | null;
  requestGeneration: number;
  serverLanguages: Set<string>;
  filePath: string | null;
}

export interface PullDiagnosticsOptions {
  changeDebounceMs?: number;
  refreshPollIntervalMs?: number;
}

interface PullDiagnosticPosition {
  line?: unknown;
  character?: unknown;
}

interface PullDiagnosticRange {
  start?: PullDiagnosticPosition;
  end?: PullDiagnosticPosition;
}

export interface PullDiagnostic extends Diagnostic {
  range?: PullDiagnosticRange | null;
}

export type LSPCodeAction = Awaited<
  ReturnType<typeof lspService.getCodeActions>
>[number];

const PULL_DIAGNOSTICS_MARKER_OWNER = "koyori-ide-lsp-pull";
const pullDiagnosticsRegistrations = new Set<monacoEditor.IDisposable>();
let defaultPullDiagnosticsRegistration: monacoEditor.IDisposable | null = null;

/**
 * G-FEAT-02: LSP store — manages offline language server status and provides
 * completions to the Monaco editor.
 *
 * Coexistence with AI inline completion:
 *   - AI uses registerInlineCompletionsProvider (ghost text)
 *   - LSP uses registerCompletionItemProvider (popup list)
 * These are different Monaco APIs and do not conflict by design.
 *
 * Graceful fallback: all query methods return empty results when the server is
 * not running or not installed, so the editor degrades smoothly.
 */

export interface LSPState {
  /** Per-server status, keyed by its canonical language/server key. */
  statuses: Record<string, LSPServerStatus>;
  /** True while a detect/start/stop operation is in flight. */
  busy: boolean;
  /** Whether LSP-backed completion is enabled (bound to settings). */
  enabled: boolean;
}

export const lspState = reactive<LSPState>({
  statuses: {},
  busy: false,
  enabled: true,
});

/** Supported languages for LSP completion. */
export type LSPLanguage =
  | "go"
  | "typescript"
  | "javascript"
  | "json"
  | "css"
  | "html"
  | "yaml"
  | "eslint"
  | "vue"
  | "angular"
  | "python"
  | "rust";

const LSP_LANGUAGE_MAP: Record<string, LSPLanguage> = {
  go: "go",
  typescript: "typescript",
  typescriptreact: "typescript",
  javascript: "javascript",
  javascriptreact: "javascript",
  json: "json",
  jsonc: "json",
  css: "css",
  scss: "css",
  less: "css",
  html: "html",
  yaml: "yaml",
  yml: "yaml",
  eslint: "eslint",
  vue: "vue",
  angular: "angular",
  python: "python",
  rust: "rust",
};

/** Returns true if any language server is available on this machine. */
export const anyLSPAvailable = computed(() =>
  Object.values(lspState.statuses).some((s) => s.available),
);

/** Returns true if any language server is currently running. */
export const anyLSPRunning = computed(() =>
  Object.values(lspState.statuses).some((s) => s.running),
);

/**
 * prompt-8 Task 8-D: compact StatusBar label for LSP.
 * e.g. "LSP: gopls ✓" / "LSP: offline" / "LSP: error".
 */
export const lspStatusLabel = computed(() => {
  if (!lspState.enabled) return "LSP: off";
  const list = Object.values(lspState.statuses);
  if (list.length === 0) return "LSP: —";
  const running = list.filter((s) => s.running);
  if (running.length > 0) {
    const kinds = running.map((s) => s.serverKind || s.language).join(",");
    return `LSP: ${kinds}`;
  }
  const err = list.find((s) => s.lastError);
  if (err?.lastError) return "LSP: error";
  const avail = list.some((s) => s.available);
  return avail ? "LSP: idle" : "LSP: n/a";
});

export const lspStatusDetail = computed(() => {
  return Object.values(lspState.statuses)
    .map((s) => {
      const st = s.running ? "running" : s.available ? "available" : "missing";
      const kind = s.serverKind || s.language;
      const detail = s.lastError || (!s.available ? s.installHint : "");
      const suffix = detail ? ` (${detail})` : "";
      const framework = s.framework ? ` [${s.framework}]` : "";
      return `${s.language}: ${kind}${framework} ${st}${suffix}`;
    })
    .join(" · ");
});

/**
 * Detect installed language servers and populate lspState.statuses.
 * Safe to call repeatedly; does not start any server.
 */
export async function detectLSPServers(): Promise<void> {
  lspState.busy = true;
  try {
    const statuses = await lspService.detectServers();
    lspState.statuses = {};
    for (const st of statuses) {
      lspState.statuses[st.language] = st;
    }
  } catch (e) {
    // Backend may be unavailable in tests / early startup — fail silently.
    console.debug("[LSP]", "detectLSPServers", e);
    pushOutput(
      "ide",
      "warn",
      `LSP detect failed: ${e instanceof Error ? e.message : String(e)}`,
    );
  } finally {
    lspState.busy = false;
  }
}

/**
 * Start the LSP server for the given language. No-op if already running or
 * not installed. Returns true on success.
 */
export async function startLSPServer(language: string): Promise<boolean> {
  if (!lspState.enabled) return false;
  const st = lspState.statuses[language];
  if (st?.running) return true;
  if (st && !st.available) return false;

  const existing = lspStartupPromises.get(language);
  if (existing) return existing;

  const startup = (async (): Promise<boolean> => {
  lspState.busy = true;
  try {
    await lspService.startServer(language);
    if (lspState.statuses[language]) {
      lspState.statuses[language].running = true;
    }
    return true;
  } catch (e) {
    console.debug("[LSP]", "startLSPServer", e);
    pushOutput(
      "ide",
      "warn",
      `LSP start ${language} failed: ${e instanceof Error ? e.message : String(e)}`,
    );
    return false;
  } finally {
    lspState.busy = false;
  }
  })();
  lspStartupPromises.set(language, startup);
  try {
    return await startup;
  } finally {
    if (lspStartupPromises.get(language) === startup) {
      lspStartupPromises.delete(language);
    }
  }
}

/**
 * Stop a running LSP server. No-op if not running.
 */
export async function stopLSPServer(language: string): Promise<void> {
  if (!lspState.statuses[language]?.running) return;
  try {
    await lspService.stopServer(language);
    if (lspState.statuses[language]) {
      lspState.statuses[language].running = false;
    }
  } catch (e) {
    console.debug("[LSP]", "stopLSPServer", e);
    pushOutput(
      "ide",
      "warn",
      `LSP stop ${language} failed: ${e instanceof Error ? e.message : String(e)}`,
    );
  }
}

/** Restart one server without disturbing any other server process. */
export async function restartLSPServer(language: string): Promise<boolean> {
  if (!lspState.enabled) return false;
  const status = lspState.statuses[language];
  if (!status?.available) return false;
  if (status.running) await stopLSPServer(language);
  return startLSPServer(language);
}

/**
 * Ensure the LSP server for a language is running, starting it lazily if it is
 * available but not yet started. Returns true if the server is running (or
 * became running), false if it is unavailable.
 */
export async function ensureLSPRunning(language: string): Promise<boolean> {
  if (!lspState.enabled) return false;
  const st = lspState.statuses[language];
  if (st?.running) return true;
  if (!st?.available) return false;
  return startLSPServer(language);
}

/**
 * Map a Monaco language id to the LSP language key. Monaco uses "typescript"
 * and "javascript" directly; Go is "go".
 *
 * 路由规则（对标 VSCode / IDEA）：
 * - .vue 文件在 Vue 项目中走 vue 服务器
 * - .html 文件在 Angular 项目中走 angular 服务器（Angular 模板）
 * - .ts/.tsx 文件始终走 typescript 服务器（提供局部变量、作用域补全）
 *   Angular 语言服务器作为辅助通过 diagnosticServerLanguages 协同工作，
 *   不抢占 TypeScript 服务器对 .ts 文件的主路由。
 *   这样 .ts 文件能获得完整的局部变量/参数/闭包补全，
 *   而非仅 Angular 模板补全。
 */
export function monacoLanguageToLSP(
  monacoLang: string,
  filePath = "",
): LSPLanguage | null {
  const normalizedPath = filePath.replace(/\\/g, "/").toLowerCase();
  const vue = lspState.statuses.vue;
  if (
    normalizedPath.endsWith(".vue") &&
    vue?.available &&
    lspStatusMatchesPath(vue, normalizedPath)
  ) {
    return "vue";
  }

  // Angular 服务器只处理 .html 模板文件，不抢占 .ts/.tsx 文件。
  // .ts/.tsx 文件由 TypeScript 服务器处理，确保局部变量、函数参数、
  // 闭包变量等作用域内补全可用（对标 VSCode 的路由策略）。
  const angular = lspState.statuses.angular;
  const isAngularTemplate = normalizedPath.endsWith(".html");
  if (
    isAngularTemplate &&
    angular?.available &&
    lspStatusMatchesPath(angular, normalizedPath)
  ) {
    return "angular";
  }
  return LSP_LANGUAGE_MAP[monacoLang.toLowerCase()] ?? null;
}

function lspStatusMatchesPath(
  status: LSPServerStatus,
  normalizedPath: string,
): boolean {
  if (!status.workspaceRoot || !normalizedPath) return true;
  const root = status.workspaceRoot
    .replace(/\\/g, "/")
    .replace(/\/$/, "")
    .toLowerCase();
  return normalizedPath === root || normalizedPath.startsWith(`${root}/`);
}

/** Return the independent servers that should contribute diagnostics. */
export function diagnosticServerLanguages(
  language: string,
  filePath = "",
): LSPLanguage[] {
  if (!lspState.enabled) return [];
  const routed = monacoLanguageToLSP(language, filePath);
  if (!routed || routed === "eslint") return routed ? [routed] : [];
  const servers: LSPLanguage[] = [];
  if (routed === "angular") {
    const primary = LSP_LANGUAGE_MAP[language.toLowerCase()];
    if (primary && primary !== "angular") servers.push(primary);
  }
  servers.push(routed);
  // Angular 项目中的 .ts/.tsx 文件：主路由是 typescript（提供局部变量补全），
  // 但 Angular 服务器仍应作为辅助提供 Angular 特定诊断（模板、指令等）。
  const normalizedPath = filePath.replace(/\\/g, "/").toLowerCase();
  const angular = lspState.statuses.angular;
  if (
    routed !== "angular" &&
    angular?.available &&
    lspStatusMatchesPath(angular, normalizedPath) &&
    (normalizedPath.endsWith(".ts") || normalizedPath.endsWith(".tsx"))
  ) {
    servers.push("angular");
  }
  if (
    servers.some(
      (server) =>
        server === "typescript" || server === "javascript" || server === "vue",
    ) &&
    lspState.statuses.eslint?.available
  ) {
    servers.push("eslint");
  }
  return servers;
}

/**
 * Query the LSP server for completions at a position. Returns an empty list
 * if the server is not running or the language is unsupported — never throws.
 *
 * This is the function the Monaco completion provider calls. It auto-starts
 * the server on first use (lazy start) when an installed server is detected.
 */
/** prompt-9 9-E / prompt-10 10-K: request sequence — ignore stale responses. */
const completionSequences = new Map<string, number>();
let hoverSeq = 0;
let definitionSeq = 0;
let workspaceSymbolSeq = 0;
const lspStartupPromises = new Map<string, Promise<boolean>>();

export async function getLSPCompletions(
  language: string,
  filePath: string,
  line: number,
  column: number,
  content: string,
  triggerKind?: 1 | 2 | 3,
  triggerCharacter?: string,
): Promise<LSPCompletionList> {
  const empty = (): LSPCompletionList => ({ items: [], isIncomplete: false });
  if (!lspState.enabled) return empty();
  const lspLang = monacoLanguageToLSP(language, filePath);
  if (!lspLang) return empty();

  const completionKey = `${lspLang}\u0000${filePath.replace(/\\/g, "/")}`;
  const seq = (completionSequences.get(completionKey) ?? 0) + 1;
  completionSequences.set(completionKey, seq);
  const running = await ensureLSPRunning(lspLang);
  if (!running) {
    pushOutput("LSP", "warn", `${lspLang}: not_running`);
    return empty();
  }

  const req: LSPCompletionRequest = {
    language: lspLang,
    filePath,
    line,
    column,
    content,
    triggerKind,
    triggerCharacter,
  };
  try {
    let response: unknown;
    try {
      response = await LSPServiceBindings.GetCompletionList(req);
    } catch {
      // 兼容尚未生成 GetCompletionList 的旧后端。
      response = await lspService.getCompletions(req);
    }
    // 9-E: drop out-of-order results
    if (completionSequences.get(completionKey) !== seq) return empty();
    // Older backends returned CompletionItem[] directly. Keep accepting that
    // shape while exposing the LSP CompletionList contract to callers.
    if (Array.isArray(response)) {
      return { items: response as LSPCompletionItem[], isIncomplete: false };
    }
    if (response && typeof response === "object") {
      const list = response as Partial<LSPCompletionList>;
      return {
        items: Array.isArray(list.items) ? list.items : [],
        isIncomplete: list.isIncomplete === true,
      };
    }
    return empty();
  } catch (e) {
    console.debug("[LSP]", "getLSPCompletions", e);
    if (completionSequences.get(completionKey) === seq) {
      pushOutput(
        "LSP",
        "error",
        `${lspLang}: rpc ${e instanceof Error ? e.message : String(e)}`,
      );
    }
    return empty();
  }
}

/**
 * Query the LSP server for hover info at a position. Returns "" if the server
 * is not running or the language is unsupported — never throws.
 */
export async function getLSPHover(
  language: string,
  filePath: string,
  line: number,
  column: number,
  content: string,
): Promise<string> {
  if (!lspState.enabled) return "";
  const lspLang = monacoLanguageToLSP(language, filePath);
  if (!lspLang) return "";

  const seq = ++hoverSeq;
  const running = await ensureLSPRunning(lspLang);
  if (!running) return "";

  const req: LSPCompletionRequest = {
    language: lspLang,
    filePath,
    line,
    column,
    content,
  };
  try {
    const text = await lspService.getHover(req);
    if (seq !== hoverSeq) return "";
    return text;
  } catch (e) {
    console.debug("[LSP]", "getLSPHover", e);
    pushOutput(
      "LSP",
      "warn",
      `getLSPHover failed: ${e instanceof Error ? e.message : String(e)}`,
    );
    return "";
  }
}

/** prompt-8 Task 8-F + prompt-10 10-K seq cancel */
export async function getLSPDefinition(
  language: string,
  filePath: string,
  line: number,
  column: number,
  content: string,
): Promise<LSPLocation[]> {
  if (!lspState.enabled) return [];
  const lspLang = monacoLanguageToLSP(language, filePath) ?? language;
  const seq = ++definitionSeq;
  if (!(await ensureLSPRunning(lspLang))) return [];
  try {
    const locs = await lspService.getDefinition({
      language: lspLang,
      filePath,
      line,
      column,
      content,
    });
    if (seq !== definitionSeq) return [];
    return locs;
  } catch (e) {
    console.debug("[LSP]", "getLSPDefinition", e);
    pushOutput(
      "LSP",
      "warn",
      `getLSPDefinition failed: ${e instanceof Error ? e.message : String(e)}`,
    );
    return [];
  }
}

async function invokePullDiagnostics(
  request: LSPCompletionRequest,
): Promise<PullDiagnostic[]> {
  const result = await LSPServiceBindings.GetPullDiagnostics(request);
  return Array.isArray(result) ? result : [];
}

async function diagnosticsRefreshVersion(language: string): Promise<number | null> {
  try {
    const value = await LSPServiceBindings.GetDiagnosticsRefreshVersion(language);
    return Number.isFinite(value) ? Math.max(0, Math.floor(value)) : null;
  } catch {
    return null;
  }
}

/** Pull diagnostics directly from textDocument/diagnostic with soft failure. */
export async function getLSPPullDiagnostics(
  language: string,
  filePath: string,
  content: string,
): Promise<PullDiagnostic[]> {
  if (!lspState.enabled || !(await ensureLSPRunning(language))) return [];
  const request: LSPCompletionRequest = {
    language,
    filePath,
    line: 0,
    column: 0,
    content,
  };
  try {
    return await invokePullDiagnostics(request);
  } catch (error) {
    console.debug("[LSP]", "getLSPPullDiagnostics", language, error);
    // Pull is preferred, but servers without document diagnostics must retain
    // the latest publishDiagnostics result instead of clearing visible errors.
    try {
      return (await lspService.getDiagnostics(
        request,
      )) as unknown as PullDiagnostic[];
    } catch (pushError) {
      console.debug("[LSP]", "getLSPPushDiagnostics", language, pushError);
      return [];
    }
  }
}

function markerSeverity(
  monaco: MonacoApi,
  severity: number,
): monacoEditor.MarkerSeverity {
  if (severity === 1) return monaco.MarkerSeverity.Error;
  if (severity === 2) return monaco.MarkerSeverity.Warning;
  if (severity === 3) return monaco.MarkerSeverity.Info;
  return monaco.MarkerSeverity.Hint;
}

function diagnosticCoordinate(value: unknown, fallback: unknown): number {
  const candidate =
    typeof value === "number" && Number.isFinite(value) ? value : fallback;
  return typeof candidate === "number" && Number.isFinite(candidate)
    ? Math.max(0, Math.floor(candidate))
    : 0;
}

function diagnosticMarkerCode(
  monaco: MonacoApi,
  diagnostic: PullDiagnostic,
): monacoEditor.editor.IMarkerData["code"] {
  if (diagnostic.code === undefined || diagnostic.code === null) return undefined;
  const value = String(diagnostic.code);
  const href = diagnostic.codeDescription?.href;
  if (!href) return value;
  try {
    return { value, target: monaco.Uri.parse(href) };
  } catch {
    return value;
  }
}

function diagnosticMarkerTags(
  monaco: MonacoApi,
  diagnostic: PullDiagnostic,
): monacoEditor.MarkerTag[] | undefined {
  const tags = diagnostic.tags
    ?.filter((tag) => tag === 1 || tag === 2)
    .map((tag) =>
      tag === 1 ? monaco.MarkerTag.Unnecessary : monaco.MarkerTag.Deprecated,
    );
  return tags?.length ? tags : undefined;
}

function diagnosticRelatedInformation(
  monaco: MonacoApi,
  diagnostic: PullDiagnostic,
): monacoEditor.editor.IRelatedInformation[] | undefined {
  const related: monacoEditor.editor.IRelatedInformation[] = [];
  for (const item of diagnostic.relatedInformation ?? []) {
    const uri = item.location?.uri;
    const range = item.location?.range;
    if (!uri || !range || typeof item.message !== "string") continue;
    try {
      const startLine = diagnosticCoordinate(range.start?.line, 0);
      const startColumn = diagnosticCoordinate(range.start?.character, 0);
      const endLine = diagnosticCoordinate(range.end?.line, startLine);
      const endColumn = diagnosticCoordinate(range.end?.character, startColumn);
      const normalizedEndLine = Math.max(startLine, endLine);
      related.push({
        resource: monaco.Uri.parse(uri),
        message: item.message,
        startLineNumber: startLine + 1,
        startColumn: startColumn + 1,
        endLineNumber: normalizedEndLine + 1,
        endColumn:
          Math.max(
            normalizedEndLine === startLine ? startColumn : 0,
            endColumn,
          ) + 1,
      });
    } catch {
      // Ignore a malformed related-information URI without dropping the
      // primary diagnostic marker.
    }
  }
  return related.length ? related : undefined;
}

function diagnosticToMarker(
  monaco: MonacoApi,
  diagnostic: PullDiagnostic,
  modelVersionId: number,
): monacoEditor.editor.IMarkerData {
  const startLine = diagnosticCoordinate(
    diagnostic.range?.start?.line,
    diagnostic.line,
  );
  const startColumn = diagnosticCoordinate(
    diagnostic.range?.start?.character,
    diagnostic.column,
  );
  const endLine = diagnosticCoordinate(
    diagnostic.range?.end?.line,
    diagnostic.endLine,
  );
  const endColumn = diagnosticCoordinate(
    diagnostic.range?.end?.character,
    diagnostic.endColumn,
  );
  const startLineNumber = startLine + 1;
  const startColumnNumber = startColumn + 1;
  const normalizedEndLine = Math.max(startLine, endLine);
  const endLineNumber = normalizedEndLine + 1;
  const endColumnNumber =
    Math.max(
      normalizedEndLine === startLine ? startColumn : 0,
      endColumn,
    ) + 1;
  const code = diagnosticMarkerCode(monaco, diagnostic);
  const relatedInformation = diagnosticRelatedInformation(monaco, diagnostic);
  const tags = diagnosticMarkerTags(monaco, diagnostic);
  return {
    severity: markerSeverity(monaco, diagnostic.severity),
    message: diagnostic.message,
    source: diagnostic.source || "LSP",
    startLineNumber,
    startColumn: startColumnNumber,
    endLineNumber,
    endColumn: endColumnNumber,
    modelVersionId,
    ...(code !== undefined ? { code } : {}),
    ...(relatedInformation ? { relatedInformation } : {}),
    ...(tags ? { tags } : {}),
  };
}

function physicalDiagnosticModelPath(
  model: monacoEditor.editor.ITextModel,
): string | null {
  const uriText = model.uri.toString();
  const uriPath = model.uri.path || uriText;
  const isVirtual =
    uriText.startsWith("inmemory:") || uriText.startsWith("untitled:");
  const fsPath = model.uri.fsPath;
  if (!isVirtual && fsPath && !fsPath.startsWith("inmemory:")) return fsPath;
  if (isVirtual) return null;
  if (/^\/[A-Za-z]:\//.test(uriPath)) {
    return uriPath.slice(1).replace(/\//g, "\\");
  }
  return uriPath;
}

async function resolveDiagnosticModelPath(
  model: monacoEditor.editor.ITextModel,
  content: string,
): Promise<string | null> {
  const physicalPath = physicalDiagnosticModelPath(model);
  if (physicalPath) return physicalPath;

  try {
    const { editorState } = await import("@/stores/editor");
    const matches = editorState.openFiles.filter((file) => file.content === content);
    if (matches.length === 1) return matches[0].path;
  } catch {
    // The editor store can be unavailable during early bootstrap and tests.
  }
  return null;
}

function normalizeDiagnosticPath(filePath: string): string {
  const normalized = filePath.replace(/\\/g, "/");
  return /^[A-Za-z]:\//.test(normalized) || normalized.startsWith("//")
    ? normalized.toLowerCase()
    : normalized;
}

function eventPayload(value: unknown): unknown {
  if (value && typeof value === "object" && "data" in value) {
    return (value as { data?: unknown }).data;
  }
  return value;
}

function refreshEventDetails(value: unknown): {
  language: string | null;
  version: number | null;
} {
  const payload = eventPayload(value);
  if (!payload || typeof payload !== "object" || !("language" in payload)) {
    return { language: null, version: null };
  }
  const fields = payload as { language?: unknown; version?: unknown };
  const language =
    typeof fields.language === "string" && fields.language
      ? fields.language
      : null;
  const version =
    typeof fields.version === "number" && Number.isFinite(fields.version)
      ? Math.max(0, Math.floor(fields.version))
      : null;
  return { language, version };
}

function savedEventPath(value: unknown): string | null {
  const payload = eventPayload(value);
  return typeof payload === "string" && payload ? payload : null;
}

/**
 * Register proactive Pull Diagnostics for Monaco models. Model creation,
 * content changes, language changes and server refresh requests all converge
 * on the same marker update path.
 */
export function registerPullDiagnostics(
  monaco: MonacoApi = defaultMonaco,
  options: PullDiagnosticsOptions = {},
): monacoEditor.IDisposable {
  const changeDebounceMs = Math.max(0, options.changeDebounceMs ?? 500);
  const refreshPollIntervalMs = Math.max(
    0,
    options.refreshPollIntervalMs ?? 1000,
  );
  const modelStates = new Map<
    monacoEditor.editor.ITextModel,
    PullDiagnosticsModelState
  >();
  const lifecycleDisposables: monacoEditor.IDisposable[] = [];
  const refreshVersions = new Map<string, number>();
  let disposed = false;
  let polling = false;
  let pollTimer: ReturnType<typeof setInterval> | null = null;
  const eventCancellers = new Set<() => void>();

  const supportsModelLifecycle =
    typeof monaco.editor.getModels === "function" &&
    typeof monaco.editor.onDidCreateModel === "function" &&
    typeof monaco.editor.onWillDisposeModel === "function" &&
    typeof monaco.editor.setModelMarkers === "function";

  const clearTimer = (state: PullDiagnosticsModelState): void => {
    if (state.timer === null) return;
    clearTimeout(state.timer);
    state.timer = null;
  };

  const detachModel = (model: monacoEditor.editor.ITextModel): void => {
    const state = modelStates.get(model);
    if (!state) return;
    clearTimer(state);
    state.contentDisposable?.dispose();
    state.contentDisposable = null;
    state.requestGeneration += 1;
    state.filePath = null;
    if (!model.isDisposed?.()) {
      monaco.editor.setModelMarkers(model, PULL_DIAGNOSTICS_MARKER_OWNER, []);
    }
    modelStates.delete(model);
  };

  const refreshModel = async (
    model: monacoEditor.editor.ITextModel,
    state: PullDiagnosticsModelState,
  ): Promise<void> => {
    if (disposed || model.isDisposed?.() || !lspState.enabled) return;
    try {
      const generation = ++state.requestGeneration;
      const content = model.getValue();
      const modelVersionId = model.getVersionId?.() ?? 0;
      const filePath = await resolveDiagnosticModelPath(model, content);
      if (
        disposed ||
        model.isDisposed?.() ||
        state.requestGeneration !== generation ||
        (model.getVersionId?.() ?? 0) !== modelVersionId
      ) {
        return;
      }
      if (!filePath) {
        state.serverLanguages.clear();
        state.filePath = null;
        monaco.editor.setModelMarkers(
          model,
          PULL_DIAGNOSTICS_MARKER_OWNER,
          [],
        );
        return;
      }
      state.filePath = filePath;
      const servers = diagnosticServerLanguages(
        model.getLanguageId(),
        filePath,
      );
      state.serverLanguages = new Set(servers);
      const results = await Promise.all(
        servers.map(async (server) =>
          getLSPPullDiagnostics(server, filePath, content),
        ),
      );
      if (
        disposed ||
        model.isDisposed?.() ||
        state.requestGeneration !== generation ||
        (model.getVersionId?.() ?? 0) !== modelVersionId
      ) {
        return;
      }
      const markers = results.flatMap((diagnostics) =>
        diagnostics.map((diagnostic) =>
          diagnosticToMarker(monaco, diagnostic, modelVersionId),
        ),
      );
      monaco.editor.setModelMarkers(
        model,
        PULL_DIAGNOSTICS_MARKER_OWNER,
        markers,
      );
    } catch (error) {
      // Model teardown and host shutdown are soft failures for background
      // diagnostics. The timer callback must never leak a rejected promise.
      if (!disposed) {
        console.debug("[LSP]", "refreshPullDiagnostics", error);
      }
    }
  };

  const scheduleModel = (
    model: monacoEditor.editor.ITextModel,
    delay: number,
  ): void => {
    const state = modelStates.get(model);
    if (!state || disposed || model.isDisposed?.()) return;
    clearTimer(state);
    state.timer = setTimeout(() => {
      state.timer = null;
      void refreshModel(model, state);
    }, delay);
  };

  const attachModel = (model: monacoEditor.editor.ITextModel): void => {
    if (disposed || model.isDisposed?.() || modelStates.has(model)) return;
    const state: PullDiagnosticsModelState = {
      contentDisposable: null,
      timer: null,
      requestGeneration: 0,
      serverLanguages: new Set(),
      filePath: physicalDiagnosticModelPath(model),
    };
    modelStates.set(model, state);
    if (typeof model.onDidChangeContent === "function") {
      state.contentDisposable = model.onDidChangeContent(() => {
        scheduleModel(model, changeDebounceMs);
      });
    }
    scheduleModel(model, 0);
  };

  const scheduleLanguage = (language: string | null): void => {
    for (const [model, state] of modelStates) {
      if (
        !language ||
        state.serverLanguages.size === 0 ||
        state.serverLanguages.has(language)
      ) {
        scheduleModel(model, 0);
      }
    }
  };

  const scheduleSavedFile = (filePath: string): void => {
    const normalizedSavedPath = normalizeDiagnosticPath(filePath);
    for (const [model, state] of modelStates) {
      const modelPath = state.filePath ?? physicalDiagnosticModelPath(model);
      if (
        modelPath &&
        normalizeDiagnosticPath(modelPath) === normalizedSavedPath
      ) {
        scheduleModel(model, 0);
      }
    }
  };

  const handleRefreshEvent = (event: unknown): void => {
    const { language, version } = refreshEventDetails(event);
    if (language && version !== null) {
      const previous = refreshVersions.get(language);
      if (previous !== undefined && version <= previous) return;
      refreshVersions.set(language, version);
    }
    scheduleLanguage(language);
  };

  const listen = (
    name: string,
    listener: (event: unknown) => void,
  ): void => {
    try {
      eventCancellers.add(Events.On(name, listener));
    } catch {
      // Wails events are unavailable in headless tests and early bootstrap.
    }
  };

  const pollRefreshVersions = async (): Promise<void> => {
    if (disposed || polling) return;
    polling = true;
    try {
      const languages = new Set<string>();
      for (const state of modelStates.values()) {
        for (const language of state.serverLanguages) languages.add(language);
      }
      await Promise.all(
        [...languages].map(async (language) => {
          const version = await diagnosticsRefreshVersion(language);
          if (version === null || disposed) return;
          const previous = refreshVersions.get(language);
          refreshVersions.set(language, version);
          if (
            (previous === undefined && version > 0) ||
            (previous !== undefined && previous !== version)
          ) {
            scheduleLanguage(language);
          }
        }),
      );
    } finally {
      polling = false;
    }
  };

  if (supportsModelLifecycle) {
    lifecycleDisposables.push(
      monaco.editor.onDidCreateModel(attachModel),
      monaco.editor.onWillDisposeModel(detachModel),
    );
    if (typeof monaco.editor.onDidChangeModelLanguage === "function") {
      lifecycleDisposables.push(
        monaco.editor.onDidChangeModelLanguage(({ model }) => {
          modelStates.get(model)?.serverLanguages.clear();
          scheduleModel(model, 0);
        }),
      );
    }
    for (const model of monaco.editor.getModels()) attachModel(model);

    listen("file:saved", (event) => {
      const filePath = savedEventPath(event);
      if (filePath) scheduleSavedFile(filePath);
    });
    listen("lsp:refreshDiagnostics", handleRefreshEvent);
    listen("lsp:refresh-diagnostics", handleRefreshEvent);
    if (refreshPollIntervalMs > 0) {
      pollTimer = setInterval(() => {
        void pollRefreshVersions();
      }, refreshPollIntervalMs);
    }
  }

  const tracked: monacoEditor.IDisposable = {
    dispose() {
      if (disposed) return;
      disposed = true;
      if (pollTimer !== null) clearInterval(pollTimer);
      pollTimer = null;
      for (const cancel of eventCancellers) cancel();
      eventCancellers.clear();
      for (const disposable of lifecycleDisposables) disposable.dispose();
      lifecycleDisposables.length = 0;
      for (const model of [...modelStates.keys()]) detachModel(model);
      pullDiagnosticsRegistrations.delete(tracked);
      if (defaultPullDiagnosticsRegistration === tracked) {
        defaultPullDiagnosticsRegistration = null;
      }
    },
  };
  pullDiagnosticsRegistrations.add(tracked);
  return tracked;
}

export function cleanupPullDiagnostics(): void {
  for (const registration of [...pullDiagnosticsRegistrations]) {
    registration.dispose();
  }
}

/**
 * prompt-10 10-D: pull publishDiagnostics cache into Problems panel.
 */
export async function refreshDiagnosticsToProblems(
  language: string,
  filePath: string,
  content: string,
): Promise<void> {
  const candidates = diagnosticServerLanguages(language, filePath);
  const readiness = await Promise.all(
    candidates.map(async (server) => ({
      server,
      running: await ensureLSPRunning(server),
    })),
  );
  const runningServers = readiness
    .filter(({ running }) => running)
    .map(({ server }) => server);
  if (runningServers.length === 0) return;

  const results = await Promise.all(
    runningServers.map(async (server) => {
      try {
        const diagnostics = await lspService.getDiagnostics({
          language: server,
          filePath,
          line: 0,
          column: 0,
          content,
        });
        return { server, diagnostics: diagnostics ?? [] };
      } catch (e) {
        console.debug("[LSP]", "refreshDiagnosticsToProblems", server, e);
        pushOutput(
          "LSP",
          "warn",
          `refreshDiagnosticsToProblems ${server} failed: ${e instanceof Error ? e.message : String(e)}`,
        );
        return { server, diagnostics: [] };
      }
    }),
  );

  const { outputState, pushProblem } = await import("@/stores/output");
  outputState.problems = outputState.problems.filter(
    (problem) =>
      problem.file !== filePath &&
      !filePath.endsWith(problem.file) &&
      !problem.file.endsWith(filePath),
  );
  for (const { server, diagnostics } of results) {
    for (const diagnostic of diagnostics) {
      const severity =
        diagnostic.severity === 1
          ? "error"
          : diagnostic.severity === 2
            ? "warning"
            : diagnostic.severity === 3
              ? "info"
              : "hint";
      pushProblem(
        severity,
        filePath,
        (diagnostic.line ?? 0) + 1,
        (diagnostic.column ?? 0) + 1,
        diagnostic.message,
        diagnostic.source || server,
      );
    }
  }
}

/** prompt-8 Task 8-F */
export async function getLSPReferences(
  language: string,
  filePath: string,
  line: number,
  column: number,
  content: string,
): Promise<LSPLocation[]> {
  if (!lspState.enabled) return [];
  const serverLanguage = monacoLanguageToLSP(language, filePath) ?? language;
  if (!(await ensureLSPRunning(serverLanguage))) return [];
  try {
    return await lspService.getReferences({
      language: serverLanguage,
      filePath,
      line,
      column,
      content,
    });
  } catch (e) {
    console.debug("[LSP]", "getLSPReferences", e);
    pushOutput(
      "LSP",
      "warn",
      `getLSPReferences failed: ${e instanceof Error ? e.message : String(e)}`,
    );
    return [];
  }
}

/** prompt-8 Task 8-G */
export async function formatLSPDocument(
  language: string,
  filePath: string,
  content: string,
): Promise<LSPTextEdit[]> {
  if (!lspState.enabled) return [];
  const serverLanguage = monacoLanguageToLSP(language, filePath) ?? language;
  if (!(await ensureLSPRunning(serverLanguage))) return [];
  try {
    return await lspService.formatDocument({
      language: serverLanguage,
      filePath,
      line: 0,
      column: 0,
      content,
    });
  } catch (e) {
    console.debug("[LSP]", "formatLSPDocument", e);
    pushOutput(
      "LSP",
      "warn",
      `formatLSPDocument failed: ${e instanceof Error ? e.message : String(e)}`,
    );
    return [];
  }
}

/** prompt-8 Task 8-A: didClose when tab closes. */
export async function closeLSPDocument(
  language: string,
  filePath: string,
): Promise<void> {
  try {
    const serverLanguage = monacoLanguageToLSP(language, filePath) ?? language;
    await lspService.closeDocument(serverLanguage, filePath);
  } catch (e) {
    console.debug("[LSP]", "closeLSPDocument", e);
    pushOutput(
      "LSP",
      "warn",
      `closeLSPDocument failed: ${e instanceof Error ? e.message : String(e)}`,
    );
  }
}

/** prompt-9 9-B multi-file rename */
export async function renameSymbolWorkspace(
  language: string,
  filePath: string,
  line: number,
  column: number,
  content: string,
  newName: string,
): Promise<Array<{ filePath: string; edits: LSPTextEdit[] }>> {
  const serverLanguage = monacoLanguageToLSP(language, filePath) ?? language;
  if (!(await ensureLSPRunning(serverLanguage))) return [];
  try {
    return await lspService.renameSymbolWorkspace(
      { language: serverLanguage, filePath, line, column, content },
      newName,
    );
  } catch (e) {
    console.debug("[LSP]", "renameSymbolWorkspace", e);
    pushOutput(
      "LSP",
      "error",
      `rename: ${e instanceof Error ? e.message : String(e)}`,
    );
    return [];
  }
}

/** prompt-9 9-G; G-HL-01: parameters upgraded to include per-parameter documentation. */
export async function getLSPSignatureHelp(
  language: string,
  filePath: string,
  line: number,
  column: number,
  content: string,
): Promise<{
  label: string;
  documentation: string;
  parameters: { label: string; documentation: string }[];
  activeParameter: number;
  activeSignature: number;
} | null> {
  const serverLanguage = monacoLanguageToLSP(language, filePath) ?? language;
  if (!(await ensureLSPRunning(serverLanguage))) return null;
  try {
    return await lspService.getSignatureHelp({
      language: serverLanguage,
      filePath,
      line,
      column,
      content,
    });
  } catch (e) {
    console.debug("[LSP]", "getLSPSignatureHelp", e);
    pushOutput(
      "LSP",
      "warn",
      `getLSPSignatureHelp failed: ${e instanceof Error ? e.message : String(e)}`,
    );
    return null;
  }
}

/** G-HL-02: Code Lens support — shows reference counts, implementations, etc. */
export async function getLSPCodeLenses(
  language: string,
  filePath: string,
  content: string,
): Promise<{ line: number; column: number; label: string; command: string }[]> {
  const serverLanguage = monacoLanguageToLSP(language, filePath) ?? language;
  if (!(await ensureLSPRunning(serverLanguage))) return [];
  try {
    return await lspService.getCodeLenses({
      language: serverLanguage,
      filePath,
      line: 0,
      column: 0,
      content,
    });
  } catch (e) {
    console.debug("[LSP]", "getLSPCodeLenses", e);
    pushOutput(
      "LSP",
      "warn",
      `getLSPCodeLenses failed: ${e instanceof Error ? e.message : String(e)}`,
    );
    return [];
  }
}

/** prompt-9 9-G organize imports */
export async function organizeLSPImports(
  language: string,
  filePath: string,
  content: string,
): Promise<LSPTextEdit[]> {
  const serverLanguage = monacoLanguageToLSP(language, filePath) ?? language;
  if (!(await ensureLSPRunning(serverLanguage))) return [];
  try {
    return await lspService.organizeImports({
      language: serverLanguage,
      filePath,
      line: 0,
      column: 0,
      content,
    });
  } catch (e) {
    console.debug("[LSP]", "organizeLSPImports", e);
    pushOutput(
      "LSP",
      "warn",
      `organizeLSPImports failed: ${e instanceof Error ? e.message : String(e)}`,
    );
    return [];
  }
}

// ============================================================================
// G-COMP-02: Document symbols, workspace symbols, semantic tokens, completion
// item resolution. All methods degrade gracefully (empty results) when the
// server is unavailable.
// ============================================================================

/** Get the document outline (textDocument/documentSymbol) for the open file. */
export async function getLSPDocumentSymbols(
  language: string,
  filePath: string,
  content: string,
): Promise<LSPDocumentSymbol[]> {
  const serverLanguage = monacoLanguageToLSP(language, filePath) ?? language;
  if (!(await ensureLSPRunning(serverLanguage))) return [];
  try {
    return await lspService.getDocumentSymbols({
      language: serverLanguage,
      filePath,
      line: 0,
      column: 0,
      content,
    });
  } catch (e) {
    console.debug("[LSP]", "getLSPDocumentSymbols", e);
    pushOutput(
      "LSP",
      "warn",
      `getLSPDocumentSymbols failed: ${e instanceof Error ? e.message : String(e)}`,
    );
    return [];
  }
}

/** Search workspace symbols (workspace/symbol, Ctrl+T). */
export async function getLSPWorkspaceSymbols(
  language: string,
  query: string,
): Promise<LSPSymbolInformation[]> {
  if (!(await ensureLSPRunning(language))) return [];
  try {
    return await lspService.getWorkspaceSymbols(language, query);
  } catch (e) {
    console.debug("[LSP]", "getLSPWorkspaceSymbols", e);
    pushOutput(
      "LSP",
      "warn",
      `getLSPWorkspaceSymbols failed: ${e instanceof Error ? e.message : String(e)}`,
    );
    return [];
  }
}

function workspaceSymbolURI(path: string): string {
  if (/^[a-z][a-z\d+.-]*:\/\//i.test(path)) return path;
  const normalized = path.replace(/\\/g, "/");
  const absolute = normalized.startsWith("/") ? normalized : `/${normalized}`;
  return `file://${encodeURI(absolute).replace(/#/g, "%23").replace(/\?/g, "%3F")}`;
}

function normalizeWorkspaceSymbol(
  symbol: WorkspaceSymbol | LSPSymbolInformation,
): WorkspaceSymbol | null {
  if (!symbol || typeof symbol !== "object") return null;
  if (
    "location" in symbol &&
    symbol.location &&
    typeof symbol.location.uri === "string" &&
    symbol.location.range
  ) {
    return symbol;
  }
  if (!("filePath" in symbol) || !symbol.filePath) return null;
  return {
    name: symbol.name,
    kind: symbol.kind,
    containerName: symbol.containerName,
    location: {
      uri: workspaceSymbolURI(symbol.filePath),
      range: {
        start: { line: symbol.line, character: symbol.column },
        end: { line: symbol.endLine, character: symbol.endColumn },
      },
    },
  };
}

/**
 * Search every active workspace language server and expose the standard LSP
 * WorkspaceSymbol shape. The caller owns the 300ms UI debounce.
 */
export async function getWorkspaceSymbols(query: string): Promise<WorkspaceSymbol[]> {
  if (!lspState.enabled || !query.trim()) return [];
  const seq = ++workspaceSymbolSeq;
  const statuses = Object.values(lspState.statuses);
  const candidates = statuses.filter(
    (status) => status.running || status.available,
  );
  const languages = [...new Set(candidates.map((status) => status.language))];
  if (languages.length === 0) return [];

  const batches = await Promise.all(
    languages.map((language) => getLSPWorkspaceSymbols(language, query)),
  );
  if (seq !== workspaceSymbolSeq) return [];

  const unique = new Map<string, WorkspaceSymbol>();
  for (const symbol of batches.flat()) {
    const normalized = normalizeWorkspaceSymbol(symbol);
    if (!normalized) continue;
    const start = normalized.location.range.start;
    const key = `${normalized.name}\u0000${normalized.location.uri}\u0000${start.line}:${start.character}`;
    unique.set(key, normalized);
  }
  return [...unique.values()];
}

/** Get semantic tokens for semantic highlighting (textDocument/semanticTokens/full). */
export async function getLSPSemanticTokens(
  language: string,
  filePath: string,
  content: string,
): Promise<SemanticToken[]> {
  const serverLanguage = monacoLanguageToLSP(language, filePath) ?? language;
  if (!(await ensureLSPRunning(serverLanguage))) return [];
  try {
    const response: unknown = await lspService.getSemanticTokens({
      language: serverLanguage,
      filePath,
      line: 0,
      column: 0,
      content,
    });
    if (!Array.isArray(response)) return [];
    return response.flatMap((value): SemanticToken[] => {
      if (!value || typeof value !== "object") return [];
      const token = value as {
        line?: number;
        column?: number;
        start?: number;
        length?: number;
        type?: number;
        modifiers?: number | number[];
      };
      const start = token.start ?? token.column;
      if (
        token.line === undefined ||
        start === undefined ||
        token.length === undefined ||
        token.type === undefined
      ) {
        return [];
      }
      const modifiers = Array.isArray(token.modifiers)
        ? token.modifiers.reduce(
            (mask, modifier) =>
              Number.isInteger(modifier) && modifier >= 0 && modifier < 32
                ? (mask | (1 << modifier)) >>> 0
                : mask,
            0,
          )
        : (token.modifiers ?? 0) >>> 0;
      return [
        {
          line: token.line,
          start,
          length: token.length,
          type: token.type,
          modifiers,
        },
      ];
    });
  } catch (e) {
    console.debug("[LSP]", "getLSPSemanticTokens", e);
    pushOutput(
      "LSP",
      "warn",
      `getLSPSemanticTokens failed: ${e instanceof Error ? e.message : String(e)}`,
    );
    return [];
  }
}

/**
 * Priority 1: Get inlay hints (textDocument/inlayHint) — inline type/parameter
 * annotations. Returns empty when the server is unavailable or unsupported.
 * Never throws; errors are logged via pushOutput (M-28 pattern).
 */
export async function getLSPInlayHints(
  language: string,
  filePath: string,
  content: string,
  startLine = 0,
  endLine?: number,
): Promise<InlayHint[]> {
  if (!lspState.enabled) return [];
  const serverLanguage = monacoLanguageToLSP(language, filePath) ?? language;
  if (!(await ensureLSPRunning(serverLanguage))) return [];
  const req: LSPCompletionRequest = {
    language: serverLanguage,
    filePath,
    line: Math.max(0, startLine),
    column: 0,
    endLine,
    content,
  };
  try {
    const raw = await LSPServiceBindings.GetInlayHintsRaw(req);
    const parsed = parseRawLSPArray(raw);
    if (parsed) return normalizeInlayHints(parsed);
    return normalizeInlayHints(await lspService.getInlayHints(req));
  } catch (rawError) {
    try {
      return normalizeInlayHints(await lspService.getInlayHints(req));
    } catch (e) {
      console.debug("[LSP]", "getLSPInlayHints", rawError, e);
      pushOutput(
        "LSP",
        "warn",
        `getLSPInlayHints failed: ${e instanceof Error ? e.message : String(e)}`,
      );
      return [];
    }
  }
}

function normalizeInlayHints(values: unknown): InlayHint[] {
  if (!Array.isArray(values)) return [];
  return values.flatMap((value): InlayHint[] => {
    if (!value || typeof value !== "object") return [];
    const hint = value as {
      position?: { line?: number; character?: number };
      line?: number;
      column?: number;
      label?: unknown;
      kind?: number;
      paddingLeft?: boolean;
      paddingRight?: boolean;
      tooltip?: unknown;
      textEdits?: unknown[];
      data?: unknown;
    };
    const line = hint.position?.line ?? hint.line;
    const character = hint.position?.character ?? hint.column;
    if (line === undefined || character === undefined) return [];

    let label: InlayHint["label"];
    if (typeof hint.label === "string") {
      label = hint.label;
    } else if (Array.isArray(hint.label)) {
      const parts = hint.label.flatMap((part) => {
        if (
          !part ||
          typeof part !== "object" ||
          !("value" in part) ||
          typeof part.value !== "string"
        ) {
          return [];
        }
        const source = part as {
          value: string;
          tooltip?: unknown;
          location?: InlayHintLabelPart["location"];
        };
        const tooltip = tooltipText(source.tooltip);
        return [
          {
            value: source.value,
            ...(tooltip !== undefined ? { tooltip } : {}),
            ...(source.location ? { location: source.location } : {}),
          },
        ];
      });
      if (parts.length !== hint.label.length) return [];
      label = parts;
    } else {
      return [];
    }

    const textEdits = hint.textEdits?.flatMap((edit): LSPTextEdit[] => {
      if (!edit || typeof edit !== "object") return [];
      const source = edit as {
        range?: LSPRange;
        startLine?: number;
        startCol?: number;
        endLine?: number;
        endCol?: number;
        newText?: string;
      };
      if (typeof source.newText !== "string") return [];
      if (source.range) {
        return [
          {
            startLine: source.range.start.line,
            startCol: source.range.start.character,
            endLine: source.range.end.line,
            endCol: source.range.end.character,
            newText: source.newText,
          },
        ];
      }
      if (
        source.startLine === undefined ||
        source.startCol === undefined ||
        source.endLine === undefined ||
        source.endCol === undefined
      ) {
        return [];
      }
      return [
        {
          startLine: source.startLine,
          startCol: source.startCol,
          endLine: source.endLine,
          endCol: source.endCol,
          newText: source.newText,
        },
      ];
    });
    const normalized: InlayHint = {
      position: { line, character },
      label,
    };
    if (hint.kind !== undefined) normalized.kind = hint.kind;
    if (hint.paddingLeft !== undefined) {
      normalized.paddingLeft = hint.paddingLeft;
    }
    if (hint.paddingRight !== undefined) {
      normalized.paddingRight = hint.paddingRight;
    }
    const tooltip = tooltipText(hint.tooltip);
    if (tooltip !== undefined) normalized.tooltip = tooltip;
    if (textEdits?.length) normalized.textEdits = textEdits;
    if (hint.data !== undefined) normalized.data = hint.data;
    return [normalized];
  });
}

function tooltipText(value: unknown): string | undefined {
  if (typeof value === "string") return value || undefined;
  if (value && typeof value === "object" && "value" in value) {
    const text = (value as { value?: unknown }).value;
    return typeof text === "string" ? text : undefined;
  }
  return undefined;
}

function parseRawLSPArray(value: unknown): unknown[] | null {
  if (Array.isArray(value)) return value;
  if (value instanceof Uint8Array) {
    try {
      return parseRawLSPArray(new TextDecoder().decode(value));
    } catch {
      return null;
    }
  }
  if (typeof value !== "string") return null;
  try {
    const parsed: unknown = JSON.parse(value);
    return Array.isArray(parsed) ? parsed : null;
  } catch {
    try {
      const decoded = globalThis.atob?.(value);
      if (!decoded) return null;
      const parsed: unknown = JSON.parse(decoded);
      return Array.isArray(parsed) ? parsed : null;
    } catch {
      return null;
    }
  }
}

/** Resolve additional details (documentation/detail) for a completion item. */
export async function resolveLSPCompletionItem(
  language: string,
  item: LSPCompletionItem,
): Promise<LSPCompletionItem> {
  if (!(await ensureLSPRunning(language))) return item;
  try {
    return await lspService.resolveCompletionItem(language, item);
  } catch (e) {
    console.debug("[LSP]", "resolveLSPCompletionItem", e);
    pushOutput(
      "LSP",
      "warn",
      `resolveLSPCompletionItem failed: ${e instanceof Error ? e.message : String(e)}`,
    );
    return item;
  }
}

/** G-ACT-01: Get code actions (quick fixes, refactors) for a position. */
export async function getLSPCodeActions(
  language: string,
  filePath: string,
  line: number,
  column: number,
  content: string,
  options: {
    endLine?: number;
    endColumn?: number;
    only?: string[];
  } = {},
): Promise<LSPCodeAction[]> {
  if (!lspState.enabled) return [];
  const serverLanguage = monacoLanguageToLSP(language, filePath) ?? language;
  if (!(await ensureLSPRunning(serverLanguage))) return [];
  try {
    return await lspService.getCodeActions({
      language: serverLanguage,
      filePath,
      line,
      column,
      endLine: options.endLine,
      endColumn: options.endColumn,
      only: options.only,
      content,
    });
  } catch (e) {
    console.debug("[LSP]", "getLSPCodeActions", e);
    pushOutput(
      "LSP",
      "warn",
      `getLSPCodeActions failed: ${e instanceof Error ? e.message : String(e)}`,
    );
    return [];
  }
}

async function invokeResolveCodeAction(
  request: LSPCompletionRequest,
  action: LSPCodeAction,
): Promise<LSPCodeAction | null> {
  return lspService.resolveCodeAction(request, action);
}

/** Resolve a lazy data-only Code Action while preserving the unsaved buffer. */
export async function resolveLSPCodeAction(
  request: LSPCompletionRequest,
  action: LSPCodeAction,
): Promise<LSPCodeAction> {
  if (
    !lspState.enabled ||
    action.disabled ||
    action.data === undefined ||
    action.data === null
  ) {
    return action;
  }
  const language =
    monacoLanguageToLSP(request.language, request.filePath) ?? request.language;
  if (!(await ensureLSPRunning(language))) return action;
  const resolvedRequest = { ...request, language };
  try {
    const resolved = await invokeResolveCodeAction(resolvedRequest, action);
    return resolved && typeof resolved.title === "string" ? resolved : action;
  } catch (error) {
    console.debug("[LSP]", "resolveLSPCodeAction", error);
    pushOutput(
      "LSP",
      "warn",
      `resolveLSPCodeAction failed: ${error instanceof Error ? error.message : String(error)}`,
    );
    return action;
  }
}

/** G-ACT-02: Go to Implementation (textDocument/implementation). */
export async function getLSPImplementation(
  language: string,
  filePath: string,
  line: number,
  column: number,
  content: string,
): Promise<LSPLocation[]> {
  if (!lspState.enabled) return [];
  const serverLanguage = monacoLanguageToLSP(language, filePath) ?? language;
  if (!(await ensureLSPRunning(serverLanguage))) return [];
  try {
    return await lspService.getImplementation({
      language: serverLanguage,
      filePath,
      line,
      column,
      content,
    });
  } catch (e) {
    console.debug("[LSP]", "getLSPImplementation", e);
    pushOutput(
      "LSP",
      "warn",
      `getLSPImplementation failed: ${e instanceof Error ? e.message : String(e)}`,
    );
    return [];
  }
}

// ============================================================================
// Architecture C (prompt-1.md 491-500): 新增 LSP 请求转发方法。
// declaration / typeDefinition / documentLink / selectionRange / foldingRange。
// 所有方法在服务器不可用时返回空结果（graceful degradation），不抛异常。
// ============================================================================

/** Architecture C: Go to Declaration (textDocument/declaration). */
export async function getLSPDeclaration(
  language: string,
  filePath: string,
  line: number,
  column: number,
  content: string,
): Promise<LSPLocation[]> {
  if (!lspState.enabled) return [];
  const serverLanguage = monacoLanguageToLSP(language, filePath) ?? language;
  if (!(await ensureLSPRunning(serverLanguage))) return [];
  try {
    return await lspService.getDeclaration({
      language: serverLanguage,
      filePath,
      line,
      column,
      content,
    });
  } catch (e) {
    console.debug("[LSP]", "getLSPDeclaration", e);
    pushOutput(
      "LSP",
      "warn",
      `getLSPDeclaration failed: ${e instanceof Error ? e.message : String(e)}`,
    );
    return [];
  }
}

/** Architecture C: Go to Type Definition (textDocument/typeDefinition). */
export async function getLSPTypeDefinition(
  language: string,
  filePath: string,
  line: number,
  column: number,
  content: string,
): Promise<LSPLocation[]> {
  if (!lspState.enabled) return [];
  const serverLanguage = monacoLanguageToLSP(language, filePath) ?? language;
  if (!(await ensureLSPRunning(serverLanguage))) return [];
  try {
    return await lspService.getTypeDefinition({
      language: serverLanguage,
      filePath,
      line,
      column,
      content,
    });
  } catch (e) {
    console.debug("[LSP]", "getLSPTypeDefinition", e);
    pushOutput(
      "LSP",
      "warn",
      `getLSPTypeDefinition failed: ${e instanceof Error ? e.message : String(e)}`,
    );
    return [];
  }
}

/** Architecture C: Document Links (textDocument/documentLink). */
export async function getLSPDocumentLinks(
  language: string,
  filePath: string,
  content: string,
): Promise<LSPDocumentLink[]> {
  if (!lspState.enabled) return [];
  const serverLanguage = monacoLanguageToLSP(language, filePath) ?? language;
  if (!(await ensureLSPRunning(serverLanguage))) return [];
  try {
    return await lspService.getDocumentLinks({
      language: serverLanguage,
      filePath,
      line: 0,
      column: 0,
      content,
    });
  } catch (e) {
    console.debug("[LSP]", "getLSPDocumentLinks", e);
    pushOutput(
      "LSP",
      "warn",
      `getLSPDocumentLinks failed: ${e instanceof Error ? e.message : String(e)}`,
    );
    return [];
  }
}

/** Architecture C: Selection Ranges (textDocument/selectionRange). */
export async function getLSPSelectionRanges(
  language: string,
  filePath: string,
  line: number,
  column: number,
  content: string,
): Promise<LSPSelectionRange[]> {
  if (!lspState.enabled) return [];
  const serverLanguage = monacoLanguageToLSP(language, filePath) ?? language;
  if (!(await ensureLSPRunning(serverLanguage))) return [];
  try {
    return await lspService.getSelectionRanges({
      language: serverLanguage,
      filePath,
      line,
      column,
      content,
    });
  } catch (e) {
    console.debug("[LSP]", "getLSPSelectionRanges", e);
    pushOutput(
      "LSP",
      "warn",
      `getLSPSelectionRanges failed: ${e instanceof Error ? e.message : String(e)}`,
    );
    return [];
  }
}

/** Architecture C: Folding Ranges (textDocument/foldingRange). */
export async function getLSPFoldingRanges(
  language: string,
  filePath: string,
  content: string,
): Promise<LSPFoldingRange[]> {
  if (!lspState.enabled) return [];
  const serverLanguage = monacoLanguageToLSP(language, filePath) ?? language;
  if (!(await ensureLSPRunning(serverLanguage))) return [];
  try {
    return await lspService.getFoldingRanges({
      language: serverLanguage,
      filePath,
      line: 0,
      column: 0,
      content,
    });
  } catch (e) {
    console.debug("[LSP]", "getLSPFoldingRanges", e);
    pushOutput(
      "LSP",
      "warn",
      `getLSPFoldingRanges failed: ${e instanceof Error ? e.message : String(e)}`,
    );
    return [];
  }
}

// ============================================================================
// F-1 (prompt-2.md): Call Hierarchy / Type Hierarchy wrappers。
// 所有方法在服务器不可用时返回空数组（graceful degradation），不抛异常。
// ============================================================================

/** F-1: CodeEditor action → CallHierarchyPanel 的查询参数。 */
export interface CallHierarchyQuery {
  mode: "call" | "type";
  language: string;
  filePath: string;
  line: number;
  column: number;
  content: string;
}

/** F-1: 当前活跃的 Call/Type Hierarchy 查询。null 表示无查询。 */
export const callHierarchyQuery = ref<CallHierarchyQuery | null>(null);

/** F-1: 设置查询并触发面板刷新（由 CodeEditor action 调用）。 */
export function setCallHierarchyQuery(q: CallHierarchyQuery): void {
  callHierarchyQuery.value = q;
}

/** F-1: Prepare Call Hierarchy (textDocument/prepareCallHierarchy). */
export async function prepareLSPCallHierarchy(
  language: string,
  filePath: string,
  line: number,
  column: number,
  content: string,
): Promise<LSPCallHierarchyItem[]> {
  if (!lspState.enabled) return [];
  const serverLanguage = monacoLanguageToLSP(language, filePath) ?? language;
  if (!(await ensureLSPRunning(serverLanguage))) return [];
  try {
    return await lspService.prepareCallHierarchy({
      language: serverLanguage,
      filePath,
      line,
      column,
      content,
    });
  } catch (e) {
    console.debug("[LSP]", "prepareLSPCallHierarchy", e);
    pushOutput(
      "LSP",
      "warn",
      `prepareLSPCallHierarchy failed: ${e instanceof Error ? e.message : String(e)}`,
    );
    return [];
  }
}

/** F-1: Call Hierarchy Incoming Calls (callHierarchy/incomingCalls). */
export async function getLSPCallHierarchyIncomingCalls(
  language: string,
  filePath: string,
  content: string,
  item: LSPCallHierarchyItem,
): Promise<LSPCallHierarchyIncomingCall[]> {
  if (!lspState.enabled) return [];
  const serverLanguage = monacoLanguageToLSP(language, filePath) ?? language;
  if (!(await ensureLSPRunning(serverLanguage))) return [];
  try {
    return await lspService.callHierarchyIncomingCalls(
      { language: serverLanguage, filePath, line: 0, column: 0, content },
      item,
    );
  } catch (e) {
    console.debug("[LSP]", "getLSPCallHierarchyIncomingCalls", e);
    pushOutput(
      "LSP",
      "warn",
      `getLSPCallHierarchyIncomingCalls failed: ${e instanceof Error ? e.message : String(e)}`,
    );
    return [];
  }
}

/** F-1: Call Hierarchy Outgoing Calls (callHierarchy/outgoingCalls). */
export async function getLSPCallHierarchyOutgoingCalls(
  language: string,
  filePath: string,
  content: string,
  item: LSPCallHierarchyItem,
): Promise<LSPCallHierarchyOutgoingCall[]> {
  if (!lspState.enabled) return [];
  const serverLanguage = monacoLanguageToLSP(language, filePath) ?? language;
  if (!(await ensureLSPRunning(serverLanguage))) return [];
  try {
    return await lspService.callHierarchyOutgoingCalls(
      { language: serverLanguage, filePath, line: 0, column: 0, content },
      item,
    );
  } catch (e) {
    console.debug("[LSP]", "getLSPCallHierarchyOutgoingCalls", e);
    pushOutput(
      "LSP",
      "warn",
      `getLSPCallHierarchyOutgoingCalls failed: ${e instanceof Error ? e.message : String(e)}`,
    );
    return [];
  }
}

/** prompt-3 compatibility name for textDocument/prepareCallHierarchy. */
export async function prepareCallHierarchy(
  language: string,
  filePath: string,
  line: number,
  column: number,
  content: string,
): Promise<LSPCallHierarchyItem[]> {
  return prepareLSPCallHierarchy(language, filePath, line, column, content);
}

/** prompt-3 compatibility name for callHierarchy/incomingCalls. */
export async function getCallHierarchyIncoming(
  language: string,
  filePath: string,
  content: string,
  item: LSPCallHierarchyItem,
): Promise<LSPCallHierarchyIncomingCall[]> {
  return getLSPCallHierarchyIncomingCalls(language, filePath, content, item);
}

/** prompt-3 compatibility name for callHierarchy/outgoingCalls. */
export async function getCallHierarchyOutgoing(
  language: string,
  filePath: string,
  content: string,
  item: LSPCallHierarchyItem,
): Promise<LSPCallHierarchyOutgoingCall[]> {
  return getLSPCallHierarchyOutgoingCalls(language, filePath, content, item);
}

/** F-1: Prepare Type Hierarchy (textDocument/prepareTypeHierarchy). */
export async function prepareLSPTypeHierarchy(
  language: string,
  filePath: string,
  line: number,
  column: number,
  content: string,
): Promise<LSPTypeHierarchyItem[]> {
  if (!lspState.enabled) return [];
  const serverLanguage = monacoLanguageToLSP(language, filePath) ?? language;
  if (!(await ensureLSPRunning(serverLanguage))) return [];
  try {
    return await lspService.prepareTypeHierarchy({
      language: serverLanguage,
      filePath,
      line,
      column,
      content,
    });
  } catch (e) {
    console.debug("[LSP]", "prepareLSPTypeHierarchy", e);
    pushOutput(
      "LSP",
      "warn",
      `prepareLSPTypeHierarchy failed: ${e instanceof Error ? e.message : String(e)}`,
    );
    return [];
  }
}

/** F-1: Type Hierarchy Supertypes (typeHierarchy/supertypes). */
export async function getLSPTypeHierarchySupertypes(
  language: string,
  filePath: string,
  content: string,
  item: LSPTypeHierarchyItem,
): Promise<LSPTypeHierarchyItem[]> {
  if (!lspState.enabled) return [];
  const serverLanguage = monacoLanguageToLSP(language, filePath) ?? language;
  if (!(await ensureLSPRunning(serverLanguage))) return [];
  try {
    return await lspService.typeHierarchySupertypes(
      { language: serverLanguage, filePath, line: 0, column: 0, content },
      item,
    );
  } catch (e) {
    console.debug("[LSP]", "getLSPTypeHierarchySupertypes", e);
    pushOutput(
      "LSP",
      "warn",
      `getLSPTypeHierarchySupertypes failed: ${e instanceof Error ? e.message : String(e)}`,
    );
    return [];
  }
}

/** F-1: Type Hierarchy Subtypes (typeHierarchy/subtypes). */
export async function getLSPTypeHierarchySubtypes(
  language: string,
  filePath: string,
  content: string,
  item: LSPTypeHierarchyItem,
): Promise<LSPTypeHierarchyItem[]> {
  if (!lspState.enabled) return [];
  const serverLanguage = monacoLanguageToLSP(language, filePath) ?? language;
  if (!(await ensureLSPRunning(serverLanguage))) return [];
  try {
    return await lspService.typeHierarchySubtypes(
      { language: serverLanguage, filePath, line: 0, column: 0, content },
      item,
    );
  } catch (e) {
    console.debug("[LSP]", "getLSPTypeHierarchySubtypes", e);
    pushOutput(
      "LSP",
      "warn",
      `getLSPTypeHierarchySubtypes failed: ${e instanceof Error ? e.message : String(e)}`,
    );
    return [];
  }
}

/**
 * Stop all running LSP servers. Called on app shutdown or project switch.
 */
export async function stopAllLSPServers(): Promise<void> {
  for (const lang of Object.keys(lspState.statuses)) {
    if (lspState.statuses[lang].running) {
      await stopLSPServer(lang);
    }
  }
}

/** Toggle all LSP-backed features. Disabling also reaps every running server. */
export async function setLSPEnabled(enabled: boolean): Promise<void> {
  if (lspState.enabled === enabled) return;
  lspState.enabled = enabled;
  if (!enabled) {
    cleanupPullDiagnostics();
    await stopAllLSPServers();
  } else if (!defaultPullDiagnosticsRegistration) {
    defaultPullDiagnosticsRegistration = registerPullDiagnostics(defaultMonaco);
  }
}

/**
 * Initialize the LSP store: detect installed servers. Called once during app
 * bootstrap. Errors are swallowed (best-effort).
 */
export async function initLSPStore(): Promise<void> {
  await detectLSPServers();
  if (!defaultPullDiagnosticsRegistration) {
    defaultPullDiagnosticsRegistration = registerPullDiagnostics(defaultMonaco);
  }
}

/**
 * Test-only helper: reset the store to its initial state.
 */
export function __resetLSPStoreForTesting(): void {
  cleanupPullDiagnostics();
  lspStartupPromises.clear();
  lspState.statuses = {};
  lspState.busy = false;
  lspState.enabled = true;
  // N-NEW-3: 重置请求序列号，使 helper 语义完整（"reset to initial state"）。
  // 序列号单调递增仍能丢弃过期响应，但测试间复用 store 时应回到初始值。
  completionSequences.clear();
  hoverSeq = 0;
  definitionSeq = 0;
  workspaceSymbolSeq = 0;
}

// Re-export appState touch so computed re-evaluate when the project changes.
// When a project is opened, the backend workspace root changes and previously
// detected servers may need re-detection. The caller (MainLayout / project
// open flow) should call detectLSPServers() after a project opens.
export { appState };
