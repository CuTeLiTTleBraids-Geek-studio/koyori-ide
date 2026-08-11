/**
 * prompt-11/12: in-IDE DAP client store (Delve + Node MVP).
 */
// Koyori IDE 模块 · Debug；交互服务：文件系统（FileService）、调试（DebugService）。
// 喵，这是 Koyori IDE 的 Debug 模块（前端实现）~
import { reactive } from "vue";
import { debugService, fileService } from "@/api/services";
import { pushOutput } from "@/stores/output";
import { notifyError, notifySuccess, notifyInfo } from "@/lib/notifications";
import { appState } from "@/stores/app";
import { parseJSONC } from "@/lib/jsonc";
import { translate } from "@/lib/i18n";
import { detectLanguage } from "@/lib/language";
import * as DebugServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/debugservice.js";
import type {
  DataBreakpointInfo,
  DataBreakpoint,
  ExceptionInfoResp,
  DebugSource,
  DebugModule,
  DebugCompletionItem,
  StepInTarget,
  StepInTargetSet,
  BreakpointLocation,
} from "@/types";

// F-5 + F-7 (task-1.md): re-export debug auxiliary types for components
export type {
  DataBreakpointInfo,
  DataBreakpoint,
  ExceptionInfoResp,
  DebugSource,
  DebugModule,
  DebugCompletionItem,
  StepInTarget,
  StepInTargetSet,
  BreakpointLocation,
};

export interface DebugBreakpoint {
  id: number;
  file: string;
  line: number;
  verified: boolean;
  condition?: string;
  logMessage?: string;
  message?: string;
}

export interface DebugStackFrame {
  id: number;
  name: string;
  file: string;
  line: number;
  column: number;
  presentationHint?: "normal" | "label" | "subtle" | string;
  asyncBoundary?: boolean;
}

export interface DebugAsyncStackSegment {
  generation: number;
  id: string;
  description: string;
  frames: DebugStackFrame[];
  parentId: string;
}

export interface DebugVariable {
  name: string;
  value: string;
  type: string;
  /** G14: adapter-owned DAP variablesReference (matches the backend JSON); 0/undefined means no children. */
  variablesReference?: number;
}

export interface DebugLaunchConfig {
  name: string;
  kind: "package" | "test" | "node" | "browser" | "language-pack";
  adapterId?: string;
  dir: string;
  program?: string;
  runRegex?: string;
  args?: string[];
  env?: Record<string, string>;
  stopOnEntry?: boolean;
  mode?: string;
  request?: "launch" | "attach";
  address?: string;
  preLaunchTask?: string;
  browser?: "chrome" | "edge";
  executablePath?: string;
  url?: string;
  runtimeArgs?: string[];
  targetId?: string;
  webRoot?: string;
  sourceMaps?: boolean;
  pathMappings?: Record<string, string>;
}

export interface BrowserTarget {
  id: string;
  type: string;
  title: string;
  url: string;
  webSocketDebuggerUrl?: string;
}

export interface BrowserConsoleEntry {
  generation: number;
  level: string;
  text: string;
  url: string;
  line: number;
  timestamp: number;
}

export interface BrowserNetworkEntry {
  generation: number;
  requestId: string;
  phase: string;
  method: string;
  url: string;
  status: number;
  mimeType: string;
  error: string;
  timestamp: number;
}

interface VSCodeLaunchFile {
  version?: string;
  configurations?: VSCodeLaunchConfig[];
}

interface VSCodeLaunchConfig {
  name?: string;
  type?: string;
  request?: "launch" | "attach";
  mode?: string;
  program?: string;
  cwd?: string;
  args?: string[];
  env?: Record<string, string>;
  stopOnEntry?: boolean;
  host?: string;
  port?: number;
  debugServer?: number;
  preLaunchTask?: string;
  url?: string;
  webRoot?: string;
  runtimeExecutable?: string;
  runtimeArgs?: string[];
  sourceMaps?: boolean;
  pathMapping?: Record<string, string>;
  pathMappings?: Record<string, string>;
  targetId?: string;
}

// prompt-5: 函数断点 (DAP setFunctionBreakpoints)
export interface FunctionBreakpoint {
  name: string;
  condition?: string;
  hitCondition?: string;
}

// prompt-5: 会话列表条目
export interface DebugSessionListItem {
  id: string;
  active: boolean;
  running: boolean;
  stopped: boolean;
  mode: string;
  address: string;
}

// prompt-5: 内联值 (DAP inlineValues)
export interface InlineValue {
  type: string;
  text?: string;
  name?: string;
  value?: string;
  variableReference?: number;
}

const LAUNCH_CFG_KEY = "koyori-ide.debug.launchConfigs";
// prompt-5: 持久化函数断点 (跨会话保留)
const FUNC_BP_KEY = "koyori-ide.debug.functionBreakpoints";

export const debugState = reactive({
  available: false,
  running: false,
  stopped: false,
  address: "",
  mode: "",
  message: "",
  stopReason: "",
  busy: false,
  breakpoints: [] as DebugBreakpoint[],
  stack: [] as DebugStackFrame[],
  runGeneration: 0 as number,
  stackTotalFrames: 0 as number,
  stackHasMore: false,
  supportsDelayedStackTraceLoading: false,
  supportsAsyncStackTrace: false,
  asyncStackRootId: "" as string,
  asyncStackSegments: [] as DebugAsyncStackSegment[],
  stackPageLoading: false,
  asyncStackLoading: false,
  browserTargets: [] as BrowserTarget[],
  browserTargetId: "" as string,
  browserConsole: [] as BrowserConsoleEntry[],
  browserNetwork: [] as BrowserNetworkEntry[],
  locals: [] as DebugVariable[],
  watches: [] as DebugVariable[],
  // G14: nested variables loaded per adapter-owned reference.
  expandedVariables: {} as Record<number, DebugVariable[]>,
  watchInput: "",
  evaluateInput: "",
  evaluateResult: "" as string,
  lastError: "" as string,
  launchConfigs: [] as DebugLaunchConfig[],
  activeConfigName: "" as string,
  attachAddr: "127.0.0.1:2345" as string,
  pollTimer: 0 as number | ReturnType<typeof setInterval>,
  // prompt-5: 多会话
  sessions: [] as DebugSessionListItem[],
  activeSessionID: "" as string,
  // prompt-5: 函数断点
  functionBreakpoints: [] as FunctionBreakpoint[],
  newFuncBpName: "" as string,
  newFuncBpCondition: "" as string,
  newFuncBpHitCondition: "" as string,
  // prompt-5: 内联值 (栈帧 / 变量参考)
  inlineValues: [] as InlineValue[],
  // prompt-5: 变量内联编辑 (setVariable)
  editingVarName: "" as string,
  editingVarValue: "" as string,
  editingVarRef: 0 as number,
  // F-5 (task-1.md): 数据断点
  dataBreakpoints: [] as DataBreakpoint[],
  dataBreakpointInfos: [] as DataBreakpointInfo[], // 临时候选 (右键查询结果)
  // F-7 (task-1.md): 调试辅助状态
  exceptionInfo: null as ExceptionInfoResp | null,
  loadedSources: [] as DebugSource[],
  modules: [] as DebugModule[],
  completionItems: [] as DebugCompletionItem[], // 调试控制台补全 (临时)
  stepInTargets: [] as StepInTarget[], // StepIn 目标 (临时)
  breakpointLocations: [] as BreakpointLocation[], // 可设断点位置 (临时)
  // F-5: 右键查询数据断点时的上下文 (variablesReference + name)
  dataBpQueryRef: 0 as number,
  dataBpQueryName: "" as string,
  // F-7: StepIn 选择目标时的待选 frameId
  stepInPendingFrameId: 0 as number,
  // GOAL-P1-03: the stop the current stepInTargets list was enumerated during.
  //
  // A DAP target ID is only meaningful for the stop that produced it. After a
  // resume or step the adapter's IDs may refer to nothing — or to a different
  // call site. Recording the sequence lets a selection made against a stale
  // menu be refused instead of silently applied to a different program state.
  // 0 means "no valid enumeration", which the backend never issues.
  stepInStopSequence: 0 as number,
  // GOAL-P1-03: whether the active adapter implements stepInTargets at all.
  // Node/browser CDP does not, so the UI must not offer a choice it cannot
  // deliver.
  stepInTargetsSupported: false as boolean,
});

function loadPersistedLaunchConfigs(): void {
  try {
    const raw = localStorage.getItem(LAUNCH_CFG_KEY);
    if (raw) {
      debugState.launchConfigs = JSON.parse(raw) as DebugLaunchConfig[];
    }
  } catch {
    debugState.launchConfigs = [];
  }
  if (!debugState.launchConfigs.length) {
    debugState.launchConfigs = [
      {
        name: "Go: Package",
        kind: "package",
        dir: "",
        mode: "debug",
      },
      {
        name: "Go: Test package",
        kind: "test",
        dir: "",
        mode: "test",
      },
      // prompt-13 13-I: TS debug template for test tree linkage
      {
        name: "Node: current file",
        kind: "node",
        dir: "",
        program: "",
      },
      {
        name: "Node: vitest run",
        kind: "node",
        dir: "",
        program: "",
        args: ["node_modules/vitest/vitest.mjs", "run"],
      },
      {
        name: "Language Pack: current file",
        kind: "language-pack",
        dir: "",
        program: "",
      },
    ];
  }
  // prompt-5: 同时加载持久化的函数断点
  loadFunctionBreakpoints();
}

function workspacePath(root: string, relative: string): string {
  return `${root.replace(/[\\/]+$/, "")}/${relative}`;
}

function expandLaunchVariables(value: string, projectRoot: string): string {
  const currentFile = appState.currentFilePath ?? "";
  const currentDir = currentFile.replace(/[\\/][^\\/]*$/, "");
  return value
    .replaceAll("${workspaceFolder}", projectRoot)
    .replaceAll(
      "${workspaceFolderBasename}",
      projectRoot.split(/[\\/]/).pop() ?? "",
    )
    .replaceAll("${fileDirname}", currentDir)
    .replaceAll("${file}", currentFile);
}

function convertVSCodeLaunchConfig(
  config: VSCodeLaunchConfig,
  projectRoot: string,
): DebugLaunchConfig | null {
  if (!config.name || !config.type || !config.request) return null;
  const type = config.type.toLocaleLowerCase();
  const isGo = type === "go";
  const isNode = type === "node" || type === "pwa-node";
  const isChrome = type === "chrome" || type === "pwa-chrome";
  const isEdge = type === "edge" || type === "msedge" || type === "pwa-msedge";
  const isBrowser = isChrome || isEdge;
  const isLanguagePack =
    !isGo &&
    !isNode &&
    !isBrowser &&
    config.request === "launch" &&
    !!config.program;
  if (!isGo && !isNode && !isBrowser && !isLanguagePack) return null;

  const program = config.program
    ? expandLaunchVariables(config.program, projectRoot)
    : "";
  const cwd = config.cwd ? expandLaunchVariables(config.cwd, projectRoot) : "";
  const port = config.port ?? config.debugServer;
  const address = port ? `${config.host || "127.0.0.1"}:${port}` : undefined;
  const pathMappings = Object.fromEntries(
    Object.entries(config.pathMapping ?? config.pathMappings ?? {}).map(
      ([from, to]) => [from, expandLaunchVariables(to, projectRoot)],
    ),
  );

  return {
    name: config.name,
    kind: isBrowser
      ? "browser"
      : isNode
        ? "node"
        : isLanguagePack
          ? "language-pack"
          : config.mode === "test"
            ? "test"
            : "package",
    adapterId: isLanguagePack ? type : undefined,
    dir: cwd || (isGo && program ? program : projectRoot),
    program: isNode || isLanguagePack ? program : undefined,
    runRegex: isGo && config.mode === "test" ? config.args?.[0] : undefined,
    args: config.args,
    env: config.env,
    stopOnEntry: config.stopOnEntry,
    mode: config.mode,
    request: config.request,
    address,
    preLaunchTask: config.preLaunchTask,
    browser: isChrome ? "chrome" : isEdge ? "edge" : undefined,
    executablePath: config.runtimeExecutable
      ? expandLaunchVariables(config.runtimeExecutable, projectRoot)
      : undefined,
    url: config.url
      ? expandLaunchVariables(config.url, projectRoot)
      : undefined,
    runtimeArgs: config.runtimeArgs,
    targetId: config.targetId,
    webRoot: config.webRoot
      ? expandLaunchVariables(config.webRoot, projectRoot)
      : undefined,
    sourceMaps: config.sourceMaps,
    pathMappings: Object.keys(pathMappings).length ? pathMappings : undefined,
  };
}

async function loadWorkspaceLaunchConfigs(projectRoot: string): Promise<void> {
  const path = workspacePath(projectRoot, ".vscode/launch.json");
  let raw: string;
  try {
    raw = await fileService.readFile(path);
  } catch (error: unknown) {
    const message = error instanceof Error ? error.message : String(error);
    if (/not found|does not exist|enoent/i.test(message)) {
      loadPersistedLaunchConfigs();
    } else {
      notifyError(`Failed to load ${path}: ${message}`);
    }
    return;
  }

  try {
    const parsed = parseJSONC<VSCodeLaunchFile>(raw);
    const configs = (parsed.configurations ?? [])
      .map((config) => convertVSCodeLaunchConfig(config, projectRoot))
      .filter((config): config is DebugLaunchConfig => config !== null);
    debugState.launchConfigs = configs;
    if (
      !configs.some((config) => config.name === debugState.activeConfigName)
    ) {
      debugState.activeConfigName = configs[0]?.name ?? "";
    }
  } catch (error: unknown) {
    notifyError(
      `Invalid ${path}: ${error instanceof Error ? error.message : String(error)}`,
    );
  }
}

export function loadLaunchConfigs(projectRoot?: string): void | Promise<void> {
  if (projectRoot) return loadWorkspaceLaunchConfigs(projectRoot);
  loadPersistedLaunchConfigs();
}

// prompt-5: 加载持久化的函数断点列表
export function loadFunctionBreakpoints(): void {
  try {
    const raw = localStorage.getItem(FUNC_BP_KEY);
    if (raw) {
      debugState.functionBreakpoints = JSON.parse(raw) as FunctionBreakpoint[];
    }
  } catch {
    debugState.functionBreakpoints = [];
  }
}

// prompt-5: 保存函数断点到 localStorage
export function saveFunctionBreakpoints(): void {
  try {
    localStorage.setItem(
      FUNC_BP_KEY,
      JSON.stringify(debugState.functionBreakpoints),
    );
  } catch {
    /* ignore */
  }
}

export function saveLaunchConfigs(): void {
  try {
    localStorage.setItem(
      LAUNCH_CFG_KEY,
      JSON.stringify(debugState.launchConfigs),
    );
  } catch {
    /* ignore */
  }
}

export function upsertLaunchConfig(cfg: DebugLaunchConfig): void {
  const i = debugState.launchConfigs.findIndex((c) => c.name === cfg.name);
  if (i >= 0) debugState.launchConfigs[i] = cfg;
  else debugState.launchConfigs.push(cfg);
  saveLaunchConfigs();
}

export async function refreshDebugStatus(): Promise<void> {
  try {
    debugState.available = await debugService.isAvailable();
    const st = await debugService.getState();
    applyDebugSnapshot(st);
    // prompt-5: 同步会话列表与活跃 ID (失败不阻塞主流程)
    void refreshSessions().catch(() => undefined);
  } catch {
    debugState.available = false;
  }
}

type CancellableHandle = { cancel?: (cause?: unknown) => unknown };

let activeStackPageRequest: CancellableHandle | null = null;
let activeAsyncStackRequest: CancellableHandle | null = null;
let stackPageSequence = 0;
let asyncStackSequence = 0;

export interface DebugSnapshot {
  session: {
    running: boolean;
    address: string;
    mode: string;
    message: string;
    stopped?: boolean;
    stopReason?: string;
  };
  breakpoints?: DebugBreakpoint[];
  stack?: DebugStackFrame[];
  locals?: DebugVariable[];
  watches?: DebugVariable[];
  stopReason?: string;
  lastError?: string;
  generation?: number;
  stackTotalFrames?: number;
  stackHasMore?: boolean;
  supportsDelayedStackTraceLoading?: boolean;
  supportsAsyncStackTrace?: boolean;
  asyncStackRootId?: string;
  browserTargets?: BrowserTarget[];
  browserTargetId?: string;
  browserConsole?: BrowserConsoleEntry[];
  browserNetwork?: BrowserNetworkEntry[];
}

function cancelStackLoads(): void {
  stackPageSequence += 1;
  asyncStackSequence += 1;
  const stackRequest = activeStackPageRequest;
  const asyncRequest = activeAsyncStackRequest;
  activeStackPageRequest = null;
  activeAsyncStackRequest = null;
  stackRequest?.cancel?.("debug run changed");
  asyncRequest?.cancel?.("debug run changed");
  debugState.stackPageLoading = false;
  debugState.asyncStackLoading = false;
}

export function cancelAsyncStackLoad(): void {
  asyncStackSequence += 1;
  const request = activeAsyncStackRequest;
  activeAsyncStackRequest = null;
  request?.cancel?.("async stack collapsed");
  debugState.asyncStackLoading = false;
}

export function applyDebugSnapshot(st: DebugSnapshot): void {
  const incomingGeneration = st.generation ?? debugState.runGeneration;
  const incomingAsyncRoot = st.asyncStackRootId ?? "";
  const runChanged = incomingGeneration !== debugState.runGeneration;
  const asyncRootChanged = incomingAsyncRoot !== debugState.asyncStackRootId;
  if (runChanged || asyncRootChanged) {
    cancelStackLoads();
    debugState.asyncStackSegments = [];
  }
  debugState.running = !!st.session?.running;
  debugState.address = st.session?.address || "";
  debugState.mode = st.session?.mode || "";
  debugState.message = st.session?.message || "";
  debugState.stopped = !!st.session?.stopped;
  debugState.stopReason = st.session?.stopReason || st.stopReason || "";
  debugState.breakpoints = st.breakpoints || [];
  debugState.stack = st.stack || [];
  debugState.runGeneration = incomingGeneration;
  debugState.stackTotalFrames = st.stackTotalFrames ?? debugState.stack.length;
  debugState.stackHasMore = !!st.stackHasMore;
  debugState.supportsDelayedStackTraceLoading =
    !!st.supportsDelayedStackTraceLoading;
  debugState.supportsAsyncStackTrace = !!st.supportsAsyncStackTrace;
  debugState.asyncStackRootId = incomingAsyncRoot;
  debugState.browserTargets = st.browserTargets || [];
  debugState.browserTargetId = st.browserTargetId || "";
  debugState.browserConsole = st.browserConsole || [];
  debugState.browserNetwork = st.browserNetwork || [];
  debugState.locals = st.locals || [];
  debugState.watches = st.watches || [];
  debugState.lastError = st.lastError || "";
}

function startPolling(): void {
  stopPolling();
  debugState.pollTimer = setInterval(() => {
    void debugService
      .getState()
      .then(applyDebugSnapshot)
      .catch(() => undefined);
  }, 400);
}

function stopPolling(): void {
  if (debugState.pollTimer) {
    clearInterval(debugState.pollTimer as number);
    debugState.pollTimer = 0;
  }
}

export function initDebugRuntime(): void {
  const hasActiveSession =
    debugState.running ||
    debugState.stopped ||
    debugState.sessions.some((session) => session.running || session.stopped);
  if (hasActiveSession && !debugState.pollTimer) startPolling();
}

export function cleanupDebugRuntime(): void {
  stopPolling();
  cancelStackLoads();
}

export async function launchDebugPackage(): Promise<void> {
  const dir = appState.currentProject || "";
  if (!dir) {
    notifyError(translate("debug.openGoProjectFirst"));
    return;
  }
  // 报错1: 在启动前检查 dlv 是否可用，提供友好的安装指引而非裸错误。
  try {
    const ok = await debugService.isAvailable();
    if (!ok) {
      const msg = translate("debug.dlv.notInstalled");
      notifyError(msg);
      pushOutput("Debug", "error", msg);
      return;
    }
  } catch {
    // isAvailable 检查失败时不阻断，让后续 launchPackage 抛出原始错误。
  }
  debugState.busy = true;
  try {
    const session = await debugService.launchPackage(dir);
    applySession(session);
    pushOutput("Debug", "info", session.message);
    notifySuccess(translate("debug.sessionStarted"));
    startPolling();
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
    pushOutput("Debug", "error", String(e));
  } finally {
    debugState.busy = false;
  }
}

function applySession(session: {
  running: boolean;
  address: string;
  mode: string;
  message: string;
  stopped?: boolean;
  stopReason?: string;
}): void {
  debugState.running = session.running;
  debugState.address = session.address;
  debugState.mode = session.mode;
  debugState.message = session.message;
  debugState.stopped = !!session.stopped;
  debugState.stopReason = session.stopReason || "";
}

export async function launchDebugTest(runRegex: string): Promise<void> {
  const dir = appState.currentProject || "";
  if (!dir) {
    notifyError(translate("debug.openGoProjectFirst"));
    return;
  }
  // 报错1: 在启动前检查 dlv 是否可用，提供友好的安装指引。
  try {
    const ok = await debugService.isAvailable();
    if (!ok) {
      const msg = translate("debug.dlv.notInstalled");
      notifyError(msg);
      pushOutput("Debug", "error", msg);
      return;
    }
  } catch {
    // isAvailable 检查失败时不阻断。
  }
  debugState.busy = true;
  try {
    const session = await debugService.launchTest(dir, runRegex);
    applySession(session);
    pushOutput(
      "Debug",
      "info",
      session.message + (runRegex ? ` run=${runRegex}` : ""),
    );
    notifySuccess(
      translate("debug.testStarted", {
        target: runRegex || translate("debug.allTests"),
      }),
    );
    startPolling();
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
  } finally {
    debugState.busy = false;
  }
}

export async function launchWithConfig(cfg: DebugLaunchConfig): Promise<void> {
  const dir = cfg.dir || appState.currentProject || "";
  debugState.busy = true;
  try {
    if (cfg.kind === "browser") {
      const session = await DebugServiceBindings.LaunchWithConfig({
        name: cfg.name,
        kind: cfg.kind,
        dir,
        request: cfg.request || "launch",
        browser: cfg.browser || "chrome",
        executablePath: cfg.executablePath,
        url: cfg.url,
        address: cfg.address,
        runtimeArgs: cfg.runtimeArgs,
        targetId: cfg.targetId,
        webRoot: cfg.webRoot,
        sourceMaps: !!cfg.sourceMaps,
        pathMappings: cfg.pathMappings,
        stopOnEntry: !!cfg.stopOnEntry,
      });
      applySession(session);
    } else if (cfg.request === "attach") {
      if (cfg.kind === "node" || cfg.kind === "language-pack") {
        notifyError(
          `${cfg.kind} attach configurations are not supported by the current debug backend`,
        );
        return;
      }
      if (!cfg.address) {
        notifyError("Go attach needs a host and port");
        return;
      }
      const session = await debugService.attachDelve(cfg.address);
      applySession(session);
    } else if (cfg.kind === "node") {
      const prog = cfg.program || "";
      if (!prog) {
        notifyError("Node launch needs program path");
        return;
      }
      const session = await debugService.launchNode(prog, cfg.args || []);
      applySession(session);
    } else if (cfg.kind === "language-pack") {
      const program = cfg.program || appState.currentFilePath || "";
      if (!program) {
        notifyError("Language-pack launch needs a program path");
        return;
      }
      const session = await debugService.launchWithConfig({
        ...cfg,
        kind: "language-pack",
        dir: cfg.dir || program.replace(/[\\/][^\\/]+$/, ""),
        program,
      });
      applySession(session);
    } else if (cfg.kind === "test") {
      const session = await debugService.launchTest(dir, cfg.runRegex || "");
      applySession(session);
    } else {
      const session = await debugService.launchPackage(dir);
      applySession(session);
    }
    debugState.activeConfigName = cfg.name;
    notifySuccess(`Launched: ${cfg.name}`);
    startPolling();
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
  } finally {
    debugState.busy = false;
  }
}

export async function selectBrowserTarget(targetId: string): Promise<void> {
  if (!targetId || targetId === debugState.browserTargetId) return;
  debugState.busy = true;
  try {
    await DebugServiceBindings.SelectBrowserTarget(targetId);
    await refreshDebugStatus();
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
  } finally {
    debugState.busy = false;
  }
}

export async function launchNodeProgram(
  program: string,
  args: string[] = [],
): Promise<void> {
  debugState.busy = true;
  try {
    const session = await debugService.launchNode(program, args);
    applySession(session);
    notifySuccess("Node inspect-brk started");
    pushOutput("Debug", "info", session.message);
    startPolling();
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
  } finally {
    debugState.busy = false;
  }
}

export async function launchCurrentFile(
  program: string,
  args: string[] = [],
): Promise<void> {
  const language = detectLanguage(program);
  if (language === "go") {
    await launchDebugPackage();
    return;
  }
  if (
    ["typescript", "typescriptreact", "javascript", "javascriptreact"].includes(
      language,
    )
  ) {
    await launchNodeProgram(program, args);
    return;
  }
  await launchWithConfig({
    name: "Language Pack: current file",
    kind: "language-pack",
    dir: program.replace(/[\\/][^\\/]+$/, ""),
    program,
    args,
  });
}

export async function debugTestAtCursor(
  language: string,
  filePath: string,
  line: number,
  content: string,
): Promise<void> {
  if (language !== "go") {
    // 12-F: node test debug via launch node on file when JS
    if (language === "typescript" || language === "javascript") {
      await launchNodeProgram(filePath, []);
      return;
    }
    notifyError("Debug Test at Cursor: Go or TS/JS file");
    return;
  }
  const lines = content.split(/\r?\n/);
  let parent = "";
  let sub = "";
  const funcRe = /^\s*func\s+(Test[A-Za-z0-9_]+)/;
  const runRe = /\bt\.Run\(\s*['"`]([^'"`]+)['"`]/;
  const max = Math.min(line, lines.length - 1);
  for (let i = 0; i <= max; i++) {
    const m = lines[i].match(funcRe);
    if (m) {
      parent = m[1];
      sub = "";
    }
    const r = lines[i].match(runRe);
    if (r) sub = r[1];
  }
  const regex = parent ? (sub ? `${parent}/${sub}` : parent) : "";
  if (!regex) {
    notifyError("No TestXxx found at cursor");
    return;
  }
  const dir =
    filePath.replace(/[\\/][^\\/]+$/, "") || appState.currentProject || "";
  debugState.busy = true;
  try {
    const session = await debugService.launchTest(dir, `^${regex}$`);
    applySession(session);
    pushOutput("Debug", "info", `Debug Test at Cursor: ${regex}`);
    notifySuccess(`Debugging ${regex}`);
    startPolling();
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
  } finally {
    debugState.busy = false;
  }
}

export async function restartDebugSession(): Promise<void> {
  cancelStackLoads();
  debugState.asyncStackSegments = [];
  debugState.asyncStackRootId = "";
  debugState.busy = true;
  try {
    const session = await debugService.restart();
    applySession(session);
    notifySuccess("Debug session restarted");
    startPolling();
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
  } finally {
    debugState.busy = false;
  }
}

export async function stopDebugSession(): Promise<void> {
  try {
    stopPolling();
    cancelStackLoads();
    await debugService.stop();
    debugState.running = false;
    debugState.stopped = false;
    debugState.address = "";
    debugState.mode = "";
    debugState.stopReason = "";
    debugState.stack = [];
    debugState.stackHasMore = false;
    debugState.asyncStackRootId = "";
    debugState.asyncStackSegments = [];
    debugState.locals = [];
    notifyInfo("Debug session stopped");
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
  }
}

export async function debugContinue(): Promise<void> {
  try {
    cancelStackLoads();
    await debugService.continue();
    await refreshDebugStatus();
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
  }
}

export async function debugStepOver(): Promise<void> {
  try {
    cancelStackLoads();
    await debugService.stepOver();
    await refreshDebugStatus();
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
  }
}

export async function debugStepIn(): Promise<void> {
  try {
    cancelStackLoads();
    await debugService.stepIn();
    await refreshDebugStatus();
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
  }
}

export async function debugStepOut(): Promise<void> {
  try {
    cancelStackLoads();
    await debugService.stepOut();
    await refreshDebugStatus();
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
  }
}

export async function toggleBreakpoint(
  file: string,
  line: number,
): Promise<void> {
  try {
    const bps = await debugService.toggleBreakpoint(file, line);
    debugState.breakpoints = bps || [];
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
  }
}

export async function setBreakpointCondition(
  file: string,
  line: number,
  condition: string,
): Promise<void> {
  try {
    await debugService.setBreakpointCondition(file, line, condition);
    await refreshDebugStatus();
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
  }
}

export function breakpointsForFile(filePath: string): DebugBreakpoint[] {
  if (!filePath) return [];
  const norm = filePath.replace(/\\/g, "/").toLowerCase();
  return debugState.breakpoints.filter((b) => {
    const f = (b.file || "").replace(/\\/g, "/").toLowerCase();
    return f === norm || norm.endsWith(f) || f.endsWith(norm);
  });
}

export async function selectDebugFrame(frameId: number): Promise<void> {
  try {
    await debugService.selectFrame(frameId);
    await refreshDebugStatus();
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
  }
}

export async function refreshStackAndLocals(): Promise<void> {
  try {
    await debugService.refreshStackAndLocals();
    await refreshDebugStatus();
  } catch {
    /* ignore */
  }
}

/**
 * G14: expand a nested variable using its adapter-owned variablesReference.
 * Paging (start/count) is forwarded for adapters that page large objects.
 * Returns the children, or [] when the reference is invalid.
 */
export async function fetchVariables(
  variablesReference: number,
  start = 0,
  count = 0,
): Promise<DebugVariable[]> {
  if (!debugState.running || variablesReference <= 0) return [];
  try {
    const list = await DebugServiceBindings.GetVariables(
      variablesReference,
      start,
      count,
    );
    return list ?? [];
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
    return [];
  }
}

/** G14: toggle nested-variable expansion for a reference. */
export async function toggleVariableExpansion(v: DebugVariable): Promise<void> {
  const ref = v.variablesReference ?? 0;
  if (ref <= 0) return;
  if (debugState.expandedVariables[ref]) {
    delete debugState.expandedVariables[ref];
    return;
  }
  const children = await fetchVariables(ref);
  debugState.expandedVariables[ref] = children;
}

/** Clears expanded-variable state (e.g. on stop/restart). */
export function clearExpandedVariables(): void {
  debugState.expandedVariables = {};
}

export async function loadMoreStackFrames(levels = 32): Promise<void> {
  if (
    !debugState.supportsDelayedStackTraceLoading ||
    !debugState.stackHasMore ||
    !debugState.running ||
    !debugState.stopped ||
    debugState.stackPageLoading
  ) {
    return;
  }
  const generation = debugState.runGeneration;
  const startFrame = debugState.stack.length;
  const sequence = ++stackPageSequence;
  debugState.stackPageLoading = true;
  try {
    if (
      generation !== debugState.runGeneration ||
      sequence !== stackPageSequence
    )
      return;
    const request = DebugServiceBindings.LoadStackFrames(
      generation,
      startFrame,
      levels,
    );
    activeStackPageRequest = request;
    const page = await request;
    if (
      sequence !== stackPageSequence ||
      generation !== debugState.runGeneration ||
      page.generation !== generation
    ) {
      return;
    }
    debugState.stack.push(...(page.frames || []));
    debugState.stackTotalFrames = page.totalFrames;
    debugState.stackHasMore = page.hasMore;
  } catch {
    // Cancellation and run changes are expected while stepping/restarting.
  } finally {
    if (sequence === stackPageSequence) {
      activeStackPageRequest = null;
      debugState.stackPageLoading = false;
    }
  }
}

export async function loadAsyncParentStack(
  continuationId: string,
): Promise<void> {
  if (
    !debugState.supportsAsyncStackTrace ||
    !continuationId ||
    !debugState.running ||
    !debugState.stopped ||
    debugState.asyncStackLoading
  ) {
    return;
  }
  const generation = debugState.runGeneration;
  const sequence = ++asyncStackSequence;
  debugState.asyncStackLoading = true;
  try {
    if (
      generation !== debugState.runGeneration ||
      sequence !== asyncStackSequence
    )
      return;
    const request = DebugServiceBindings.LoadAsyncStack(
      generation,
      continuationId,
    );
    activeAsyncStackRequest = request;
    const segment = await request;
    if (
      sequence !== asyncStackSequence ||
      generation !== debugState.runGeneration ||
      segment.generation !== generation
    ) {
      return;
    }
    const normalizedSegment: DebugAsyncStackSegment = {
      ...segment,
      frames: segment.frames ?? [],
      parentId: segment.parentId ?? "",
    };
    const existing = debugState.asyncStackSegments.findIndex(
      (item) => item.id === normalizedSegment.id,
    );
    if (existing >= 0)
      debugState.asyncStackSegments[existing] = normalizedSegment;
    else debugState.asyncStackSegments.push(normalizedSegment);
  } catch {
    // Cancellation and expired continuations are intentionally silent.
  } finally {
    if (sequence === asyncStackSequence) {
      activeAsyncStackRequest = null;
      debugState.asyncStackLoading = false;
    }
  }
}

export async function addWatch(expr: string): Promise<void> {
  try {
    const list = await debugService.addWatch(expr);
    debugState.watches = list || [];
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
  }
}

export async function removeWatch(expr: string): Promise<void> {
  try {
    const list = await debugService.removeWatch(expr);
    debugState.watches = list || [];
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
  }
}

export async function evaluateExpression(expr: string): Promise<void> {
  try {
    const v = await debugService.evaluate(expr);
    if (v.type === "error") {
      debugState.evaluateResult = `ERROR: ${v.value}`;
      debugState.lastError = `evaluate: ${v.value}`;
      pushOutput("Debug", "error", `evaluate ${expr}: ${v.value}`);
    } else {
      debugState.evaluateResult = `${v.name} = ${v.value}${v.type ? ` (${v.type})` : ""}`;
    }
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e);
    debugState.evaluateResult = `ERROR: ${msg}`;
    debugState.lastError = msg;
    pushOutput("Debug", "error", msg);
  }
}

/** prompt-13 13-E: probe + attach remote delve */
export async function probeAndAttachDelve(addr?: string): Promise<void> {
  const a = (addr || debugState.attachAddr || "").trim();
  if (!a) {
    notifyError("Enter host:port for remote Delve");
    return;
  }
  try {
    const probe = await debugService.probeDelveTCP(a);
    if (!probe.ok) {
      notifyError(`Probe failed: ${probe.message}`);
      pushOutput("Debug", "error", `probe ${a}: ${probe.message}`);
      return;
    }
    notifyInfo(probe.message || "port open");
    const session = await debugService.attachDelve(a);
    applySession(session);
    startPolling();
    notifySuccess(`Attached: ${a}`);
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
  }
}

/** prompt-13 13-H: export launch configs as JSON string */
export function exportLaunchConfigsJSON(): string {
  return JSON.stringify(
    { version: 1, configs: debugState.launchConfigs },
    null,
    2,
  );
}

export function importLaunchConfigsJSON(raw: string): number {
  try {
    const j = JSON.parse(raw) as
      { configs?: DebugLaunchConfig[] } | DebugLaunchConfig[];
    const list = Array.isArray(j) ? j : j.configs || [];
    let n = 0;
    for (const c of list) {
      if (c?.name && c?.kind) {
        upsertLaunchConfig(c as DebugLaunchConfig);
        n++;
      }
    }
    return n;
  } catch (e) {
    notifyError(
      "Invalid launch JSON: " + (e instanceof Error ? e.message : String(e)),
    );
    return 0;
  }
}

// ============================================================================
// prompt-5: 调试器增强 — 多会话 / 函数断点 / SetVariable / RestartFrame / InlineValues
// ============================================================================

/**
 * prompt-5: 添加函数断点。同时同步到后端 (若会话运行中)。
 * 同名函数断点已存在时跳过。
 */
export async function addFunctionBreakpoint(
  name: string,
  condition = "",
  hitCondition = "",
): Promise<void> {
  const fn = name.trim();
  if (!fn) {
    notifyError("Function name is required");
    return;
  }
  if (debugState.functionBreakpoints.some((b) => b.name === fn)) {
    notifyError(`Function breakpoint "${fn}" already exists`);
    return;
  }
  const bp: FunctionBreakpoint = { name: fn };
  if (condition.trim()) bp.condition = condition.trim();
  if (hitCondition.trim()) bp.hitCondition = hitCondition.trim();
  debugState.functionBreakpoints.push(bp);
  saveFunctionBreakpoints();
  // 同步到后端 (运行中才需要，否则会在 launch 时自动应用)
  if (debugState.running) {
    try {
      await debugService.setFunctionBreakpoints(debugState.functionBreakpoints);
    } catch (e) {
      notifyError(e instanceof Error ? e.message : String(e));
    }
  }
  debugState.newFuncBpName = "";
  debugState.newFuncBpCondition = "";
  debugState.newFuncBpHitCondition = "";
}

/** prompt-5: 删除函数断点 (按名称)。 */
export async function removeFunctionBreakpoint(name: string): Promise<void> {
  const i = debugState.functionBreakpoints.findIndex((b) => b.name === name);
  if (i < 0) return;
  debugState.functionBreakpoints.splice(i, 1);
  saveFunctionBreakpoints();
  if (debugState.running) {
    if (debugState.functionBreakpoints.length === 0) {
      // 后端要求至少 1 个，空列表直接跳过同步；空列表会在下次有断点时同步
      return;
    }
    try {
      await debugService.setFunctionBreakpoints(debugState.functionBreakpoints);
    } catch (e) {
      notifyError(e instanceof Error ? e.message : String(e));
    }
  }
}

/**
 * prompt-5: 强制将当前函数断点列表同步到后端 (用于会话已启动但尚未应用的场景)。
 * 空列表会报错 (DAP 要求至少一个断点)。
 */
export async function applyFunctionBreakpoints(): Promise<void> {
  if (!debugState.running) {
    notifyError("Start a debug session first");
    return;
  }
  if (!debugState.functionBreakpoints.length) {
    notifyError("No function breakpoints to apply");
    return;
  }
  try {
    await debugService.setFunctionBreakpoints(debugState.functionBreakpoints);
    notifySuccess(
      `Applied ${debugState.functionBreakpoints.length} function breakpoint(s)`,
    );
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
  }
}

/** prompt-5: 设置变量值 (DAP setVariable)。variablesReference 通常为 Locals 作用域的引用。 */
export async function setVariable(
  variablesReference: number,
  name: string,
  value: string,
): Promise<void> {
  if (!debugState.running) {
    notifyError("Start a debug session first");
    return;
  }
  if (!name) {
    notifyError("Variable name is required");
    return;
  }
  try {
    const newVal = await debugService.setVariable(
      variablesReference,
      name,
      value,
    );
    notifySuccess(`${name} = ${newVal}`);
    // 退出编辑模式
    debugState.editingVarName = "";
    debugState.editingVarValue = "";
    debugState.editingVarRef = 0;
    // 刷新局部变量
    await refreshStackAndLocals();
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
  }
}

/** prompt-5: 进入/退出变量内联编辑模式。 */
export function startEditVariable(
  variablesReference: number,
  name: string,
  currentValue: string,
): void {
  debugState.editingVarRef = variablesReference;
  debugState.editingVarName = name;
  debugState.editingVarValue = currentValue;
}

/** prompt-5: 取消变量编辑。 */
export function cancelEditVariable(): void {
  debugState.editingVarName = "";
  debugState.editingVarValue = "";
  debugState.editingVarRef = 0;
}

/** prompt-5: 重启栈帧 (DAP restartFrame)。会触发 stopped 事件，前端轮询自动同步。 */
export async function restartFrame(frameId: number): Promise<void> {
  if (!debugState.running) {
    notifyError("Start a debug session first");
    return;
  }
  if (!frameId || frameId <= 0) {
    notifyError("Invalid frame id");
    return;
  }
  try {
    await debugService.restartFrame(frameId);
    notifySuccess(`Restarted frame #${frameId}`);
    await refreshStackAndLocals();
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
  }
}

/** prompt-5: 拉取当前栈帧的内联值 (DAP inlineValues)。 */
export async function refreshInlineValues(
  frameId: number,
  variablesReference: number,
): Promise<void> {
  if (!debugState.running) {
    debugState.inlineValues = [];
    return;
  }
  if (frameId <= 0 && variablesReference <= 0) {
    debugState.inlineValues = [];
    return;
  }
  try {
    const list = await debugService.getInlineValues(
      frameId,
      variablesReference,
    );
    debugState.inlineValues = list || [];
  } catch {
    // 部分适配器不支持 inlineValues — 静默忽略
    debugState.inlineValues = [];
  }
}

// --- prompt-5: 多会话管理 ---

/** prompt-5: 刷新会话列表 (ListSessions + GetActiveSession)。 */
export async function refreshSessions(): Promise<void> {
  try {
    const [list, active] = await Promise.all([
      debugService.listSessions(),
      debugService.getActiveSession(),
    ]);
    debugState.sessions = list || [];
    debugState.activeSessionID = active || "";
  } catch {
    // Session discovery is best-effort during startup.
    debugState.sessions = [];
  }
}

/** prompt-5: 切换活跃会话。 */
export async function switchSession(sessionID: string): Promise<void> {
  if (!sessionID || sessionID === debugState.activeSessionID) return;
  try {
    await debugService.setActiveSession(sessionID);
    debugState.activeSessionID = sessionID;
    // 同步该会话的状态到 UI
    await refreshDebugStatus();
    notifyInfo(`Switched to session: ${sessionID}`);
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
  }
}

/** prompt-5: 启动新会话 (StartSession)。返回新会话 ID。 */
export async function startDebugSession(
  cfg: DebugLaunchConfig,
): Promise<string | null> {
  debugState.busy = true;
  try {
    const dir = cfg.dir || appState.currentProject || "";
    const id = await debugService.startSession({
      name: cfg.name,
      kind: cfg.kind,
      dir,
      program: cfg.program || "",
      runRegex: cfg.runRegex || "",
      args: cfg.args || [],
      env: cfg.env || {},
      stopOnEntry: !!cfg.stopOnEntry,
      mode: cfg.mode || "",
    });
    debugState.activeConfigName = cfg.name;
    await refreshSessions();
    await refreshDebugStatus();
    notifySuccess(`Started session: ${id}`);
    startPolling();
    return id;
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
    return null;
  } finally {
    debugState.busy = false;
  }
}

/** prompt-5: 停止并销毁指定会话 (StopSession)。活跃会话被停时自动切到下一个。 */
export async function stopDebugSessionByID(sessionID: string): Promise<void> {
  if (!sessionID) {
    notifyError("Session id required");
    return;
  }
  try {
    await debugService.stopSession(sessionID);
    await refreshSessions();
    await refreshDebugStatus();
    if (
      !debugState.running &&
      !debugState.stopped &&
      !debugState.sessions.some((session) => session.running || session.stopped)
    ) {
      stopPolling();
    }
    notifyInfo(`Stopped session: ${sessionID}`);
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
  }
}

// ============================================================================
// F-5 (task-1.md): Data Breakpoints
// ============================================================================

/**
 * F-5: 查询变量可设数据断点的信息 (DAP dataBreakpointInfo)。
 * 将结果存入 debugState.dataBreakpointInfos 供 UI 弹窗选择。
 */
export async function fetchDataBreakpointInfo(
  variablesReference: number,
  name: string,
): Promise<DataBreakpointInfo[]> {
  if (!debugState.running) {
    notifyError("Start a debug session first");
    return [];
  }
  if (variablesReference <= 0) {
    notifyError("Invalid variables reference");
    return [];
  }
  debugState.dataBpQueryRef = variablesReference;
  debugState.dataBpQueryName = name;
  try {
    const list = await debugService.dataBreakpointInfo(
      variablesReference,
      name,
    );
    debugState.dataBreakpointInfos = list || [];
    return debugState.dataBreakpointInfos;
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
    debugState.dataBreakpointInfos = [];
    return [];
  }
}

/** F-5: 将一个候选 DataBreakpointInfo 加入数据断点列表并同步后端。 */
export async function addDataBreakpoint(
  info: DataBreakpointInfo,
  accessType: string,
  condition = "",
  hitCondition = "",
): Promise<void> {
  if (!info.dataId) {
    notifyError("No dataId for data breakpoint");
    return;
  }
  if (
    debugState.dataBreakpoints.some(
      (b) => b.dataId === info.dataId && b.accessType === accessType,
    )
  ) {
    notifyError(
      `Data breakpoint already exists for ${info.dataId} (${accessType})`,
    );
    return;
  }
  const bp: DataBreakpoint = { dataId: info.dataId, accessType };
  if (condition.trim()) bp.condition = condition.trim();
  if (hitCondition.trim()) bp.hitCondition = hitCondition.trim();
  debugState.dataBreakpoints.push(bp);
  await applyDataBreakpoints();
}

/** F-5: 按 dataId + accessType 移除一个数据断点并同步后端。 */
export async function removeDataBreakpoint(
  dataId: string,
  accessType: string,
): Promise<void> {
  const i = debugState.dataBreakpoints.findIndex(
    (b) => b.dataId === dataId && b.accessType === accessType,
  );
  if (i < 0) return;
  debugState.dataBreakpoints.splice(i, 1);
  await applyDataBreakpoints();
}

/** F-5: 清空所有数据断点 (空列表通过 SetDataBreakpoints 同步)。 */
export async function clearDataBreakpoints(): Promise<void> {
  debugState.dataBreakpoints = [];
  await applyDataBreakpoints();
}

/** F-5: 将当前数据断点列表同步到后端 (SetDataBreakpoints)。 */
export async function applyDataBreakpoints(): Promise<void> {
  if (!debugState.running) return;
  try {
    await debugService.setDataBreakpoints(debugState.dataBreakpoints);
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
  }
}

// ============================================================================
// F-7 (task-1.md): Debug auxiliary capabilities
// ============================================================================

/** F-7: 获取异常停止信息 (DAP exceptionInfo)。存入 debugState.exceptionInfo。 */
export async function fetchExceptionInfo(
  threadId: number,
): Promise<ExceptionInfoResp | null> {
  if (!debugState.running) {
    debugState.exceptionInfo = null;
    return null;
  }
  if (threadId <= 0) {
    debugState.exceptionInfo = null;
    return null;
  }
  try {
    const info = await debugService.exceptionInfo(threadId);
    debugState.exceptionInfo = info;
    return info;
  } catch {
    // 适配器可能不支持 exceptionInfo — 静默忽略
    debugState.exceptionInfo = null;
    return null;
  }
}

/** F-7: 获取已加载源文件列表 (DAP loadedSources)。 */
export async function fetchLoadedSources(): Promise<DebugSource[]> {
  if (!debugState.running) {
    debugState.loadedSources = [];
    return [];
  }
  try {
    const list = await debugService.loadedSources();
    debugState.loadedSources = list || [];
    return debugState.loadedSources;
  } catch {
    debugState.loadedSources = [];
    return [];
  }
}

/** F-7: 获取已加载模块列表 (DAP modules)。 */
export async function fetchModules(): Promise<DebugModule[]> {
  if (!debugState.running) {
    debugState.modules = [];
    return [];
  }
  try {
    const list = await debugService.modules();
    debugState.modules = list || [];
    return debugState.modules;
  } catch {
    debugState.modules = [];
    return [];
  }
}

/** F-7: 调试控制台补全 (DAP completions)。结果存入 debugState.completionItems。 */
export async function fetchCompletions(
  frameId: number,
  text: string,
  column: number,
): Promise<DebugCompletionItem[]> {
  if (!debugState.running) {
    debugState.completionItems = [];
    return [];
  }
  if (frameId <= 0) {
    debugState.completionItems = [];
    return [];
  }
  try {
    const list = await debugService.completions(frameId, text, column);
    debugState.completionItems = list || [];
    return debugState.completionItems;
  } catch {
    debugState.completionItems = [];
    return [];
  }
}

/** F-7: 查询 StepIn 目标 (DAP stepInTargets)。存入 debugState.stepInTargets。 */
export async function fetchStepInTargets(
  frameId: number,
): Promise<StepInTarget[]> {
  if (!debugState.running) {
    debugState.stepInTargets = [];
    return [];
  }
  if (frameId <= 0) {
    debugState.stepInTargets = [];
    return [];
  }
  debugState.stepInPendingFrameId = frameId;
  try {
    const list = await debugService.stepInTargets(frameId);
    debugState.stepInTargets = list || [];
    return debugState.stepInTargets;
  } catch {
    debugState.stepInTargets = [];
    return [];
  }
}

/**
 * GOAL-P1-03: enumerate StepIn targets together with the stop they belong to.
 *
 * Prefer this over `fetchStepInTargets`: the bare list it returns cannot be
 * validated later, and validation is the point — the menu the user clicks must
 * be provably the menu for the *current* stop.
 */
export async function fetchStepInTargetsForStop(
  frameId: number,
): Promise<StepInTargetSet> {
  const empty: StepInTargetSet = {
    targets: [],
    stopSequence: 0,
    supported: false,
  };
  if (!debugState.running || frameId <= 0) {
    debugState.stepInTargets = [];
    debugState.stepInStopSequence = 0;
    debugState.stepInTargetsSupported = false;
    return empty;
  }
  debugState.stepInPendingFrameId = frameId;
  try {
    const set = await debugService.stepInTargetsForStop(frameId);
    debugState.stepInTargets = set.targets ?? [];
    debugState.stepInStopSequence = set.stopSequence;
    debugState.stepInTargetsSupported = set.supported;
    return { ...set, targets: debugState.stepInTargets };
  } catch {
    // Enumeration failure must not block stepping: the caller falls back to the
    // default step-in, which needs no target ID.
    debugState.stepInTargets = [];
    debugState.stepInStopSequence = 0;
    debugState.stepInTargetsSupported = false;
    return empty;
  }
}

/**
 * GOAL-P1-03: step into the user's chosen target.
 *
 * Baseline defect: `DebugPanel.onPickStepInTarget` accepted the selected ID as
 * `_targetId` and threw it away, calling the plain step-in instead — the user
 * picked overload B and the debugger entered A.
 *
 * Returns false when the step was refused (stale menu, unsupported adapter, or
 * no enumeration), so the caller can fall back to a default step-in rather than
 * leaving the user with a menu click that did nothing.
 */
export async function debugStepInTarget(targetId: number): Promise<boolean> {
  const stopSequence = debugState.stepInStopSequence;
  if (targetId <= 0 || stopSequence <= 0) return false;
  try {
    cancelStackLoads();
    await debugService.stepInWithTarget(targetId, stopSequence);
    // The menu belonged to the stop we just left; keeping it would let a second
    // click reuse an ID the adapter no longer recognises.
    debugState.stepInTargets = [];
    debugState.stepInStopSequence = 0;
    await refreshDebugStatus();
    return true;
  } catch (e) {
    notifyError(e instanceof Error ? e.message : String(e));
    debugState.stepInTargets = [];
    debugState.stepInStopSequence = 0;
    return false;
  }
}

/** F-7: 查询可设断点位置 (DAP breakpointLocations)。存入 debugState.breakpointLocations。 */
export async function fetchBreakpointLocations(
  uri: string,
  startLine: number,
  endLine: number,
): Promise<BreakpointLocation[]> {
  if (!debugState.running) {
    debugState.breakpointLocations = [];
    return [];
  }
  if (!uri || startLine <= 0) {
    debugState.breakpointLocations = [];
    return [];
  }
  try {
    const list = await debugService.breakpointLocations(
      uri,
      startLine,
      endLine,
    );
    debugState.breakpointLocations = list || [];
    return debugState.breakpointLocations;
  } catch {
    debugState.breakpointLocations = [];
    return [];
  }
}

/** F-7: 清空异常面板 (在 Continue/重启后调用)。 */
export function clearExceptionInfo(): void {
  debugState.exceptionInfo = null;
}

loadLaunchConfigs();

import.meta.hot?.dispose(cleanupDebugRuntime);
