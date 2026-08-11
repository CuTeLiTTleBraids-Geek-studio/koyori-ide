// Koyori IDE 模块 · Debug Quality；交互服务：调试（DebugService）。
// 喵，这是 Koyori IDE 的 Debug Quality 模块（前端实现）~
import * as DebugServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/debugservice.js";
import * as EslintServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/eslintservice.js";
import * as CoverageServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/coverageservice.js";
import type {
  DataBreakpoint, DataBreakpointInfo, ExceptionInfoResp, StepInTargetSet,
} from "@/types";
import { requireNonNull, unwrapNullable } from "./boundary";

// prompt-10/11: Delve DAP client (in-IDE)
type DebugSessionInfo = {
  running: boolean;
  address: string;
  mode: string;
  message: string;
  stopped?: boolean;
  stopReason?: string;
  threadId?: number;
};
type DebugBp = {
  id: number;
  file: string;
  line: number;
  verified: boolean;
  condition?: string;
  logMessage?: string;
  message?: string;
};
type DebugVar = { name: string; value: string; type: string };
type DebugStateSnapshot = {
  session: DebugSessionInfo;
  breakpoints: DebugBp[];
  stack: Array<{ id: number; name: string; file: string; line: number; column: number }>;
  locals: DebugVar[];
  watches?: DebugVar[];
  stopReason?: string;
};
// prompt-5: 函数断点 (DAP setFunctionBreakpoints)
type FunctionBreakpoint = {
  name: string;
  condition?: string;
  hitCondition?: string;
};
// prompt-5: 内联值 (DAP inlineValues)
// prompt-5: 会话列表条目 (ListSessions)
type BindingDataBreakpointInfo = NonNullable<
  Awaited<ReturnType<typeof DebugServiceBindings.DataBreakpointInfo>>
>[number];
type BindingExceptionInfo = NonNullable<
  Awaited<ReturnType<typeof DebugServiceBindings.ExceptionInfo>>
>;
type BindingExceptionDetails = NonNullable<BindingExceptionInfo["details"]>;

function fromBindingDataBreakpointInfo(
  info: BindingDataBreakpointInfo,
): DataBreakpointInfo {
  return {
    ...info,
    accessTypes: info.accessTypes ?? undefined,
  };
}

function fromBindingExceptionDetails(
  details: BindingExceptionDetails,
): NonNullable<ExceptionInfoResp["details"]> {
  return {
    ...details,
    innerException: details.innerException
      ? fromBindingExceptionDetails(details.innerException)
      : undefined,
  };
}

function fromBindingExceptionInfo(info: BindingExceptionInfo): ExceptionInfoResp {
  return {
    ...info,
    details: info.details ? fromBindingExceptionDetails(info.details) : undefined,
  };
}

export const debugService = {
  isAvailable: () => DebugServiceBindings.IsAvailable(),
  statusMessage: () => DebugServiceBindings.StatusMessage(),
  isRunning: () => DebugServiceBindings.IsRunning(),
  getSession: () => DebugServiceBindings.GetSession(),
  getState: async (): Promise<DebugStateSnapshot> => {
    const state = await DebugServiceBindings.GetState();
    return {
      ...state,
      breakpoints: state.breakpoints ?? [],
      stack: state.stack ?? [],
      locals: state.locals ?? [],
      watches: state.watches ?? [],
    };
  },
  launchPackage: (packageDir: string) =>
    DebugServiceBindings.LaunchPackage(packageDir),
  launchTest: (packageDir: string, runRegex: string) =>
    DebugServiceBindings.LaunchTest(packageDir, runRegex),
  launchNode: (program: string, args: string[]) =>
    DebugServiceBindings.LaunchNode(program, args),
  launchWithConfig: (cfg: {
    name?: string;
    kind: string;
    dir: string;
    program?: string;
    runRegex?: string;
    args?: string[];
    env?: Record<string, string>;
    stopOnEntry?: boolean;
    mode?: string;
  }) =>
    DebugServiceBindings.LaunchWithConfig({
      name: cfg.name ?? "",
      kind: cfg.kind,
      dir: cfg.dir,
      program: cfg.program ?? "",
      runRegex: cfg.runRegex ?? "",
      args: cfg.args ?? [],
      env: cfg.env ?? {},
      stopOnEntry: !!cfg.stopOnEntry,
      mode: cfg.mode ?? "",
    }),
  restart: () => DebugServiceBindings.Restart(),
  stop: () => DebugServiceBindings.Stop(),
  setBreakpoint: (file: string, line: number) =>
    DebugServiceBindings.SetBreakpoint(file, line) as Promise<DebugBp>,
  setBreakpointEx: (file: string, line: number, condition: string, logMessage: string) =>
    DebugServiceBindings.SetBreakpointEx(file, line, condition, logMessage) as Promise<DebugBp>,
  setBreakpointCondition: (file: string, line: number, condition: string) =>
    DebugServiceBindings.SetBreakpointCondition(file, line, condition) as Promise<DebugBp>,
  removeBreakpoint: (file: string, line: number) =>
    DebugServiceBindings.RemoveBreakpoint(file, line) as Promise<void>,
  toggleBreakpoint: (file: string, line: number) =>
    unwrapNullable(DebugServiceBindings.ToggleBreakpoint(file, line), []),
  listBreakpoints: () => unwrapNullable(DebugServiceBindings.ListBreakpoints(), []),
  continue: () => DebugServiceBindings.Continue() as Promise<void>,
  stepOver: () => DebugServiceBindings.StepOver() as Promise<void>,
  stepIn: () => DebugServiceBindings.StepIn() as Promise<void>,
  stepOut: () => DebugServiceBindings.StepOut() as Promise<void>,
  pause: () => DebugServiceBindings.Pause() as Promise<void>,
  refreshStackAndLocals: () => DebugServiceBindings.RefreshStackAndLocals() as Promise<void>,
  selectFrame: (frameId: number) => DebugServiceBindings.SelectFrame(frameId) as Promise<void>,
  evaluate: (expression: string) =>
    DebugServiceBindings.Evaluate(expression) as Promise<DebugVar>,
  addWatch: (expression: string) =>
    unwrapNullable(DebugServiceBindings.AddWatch(expression), []),
  removeWatch: (expression: string) =>
    unwrapNullable(DebugServiceBindings.RemoveWatch(expression), []),
  refreshWatches: () => unwrapNullable(DebugServiceBindings.RefreshWatches(), []),
  listWatches: () => unwrapNullable(DebugServiceBindings.ListWatches(), []),
  attachDelve: (addr: string) =>
    DebugServiceBindings.AttachDelve(addr) as Promise<DebugSessionInfo>,
  probeDelveTCP: async (addr: string) => {
    const probe = await requireNonNull(
      DebugServiceBindings.ProbeDelveTCP(addr),
      "DebugService.ProbeDelveTCP",
    );
    if (typeof probe.ok !== "boolean" || typeof probe.message !== "string") {
      throw new Error("DebugService.ProbeDelveTCP returned an invalid result");
    }
    return {
      ok: probe.ok,
      message: probe.message,
      address: typeof probe.address === "string" ? probe.address : undefined,
    };
  },
  clearLastError: () => DebugServiceBindings.ClearLastError() as Promise<void>,
  // prompt-5: these methods are available through the generated module only.
  setFunctionBreakpoints: (breakpoints: FunctionBreakpoint[]) =>
    DebugServiceBindings.SetFunctionBreakpoints(breakpoints),
  listFunctionBreakpoints: () =>
    unwrapNullable(DebugServiceBindings.ListFunctionBreakpoints(), []),
  setVariable: (variablesReference: number, name: string, value: string) =>
    DebugServiceBindings.SetVariable(variablesReference, name, value),
  restartFrame: (frameId: number) =>
    DebugServiceBindings.RestartFrame(frameId),
  getInlineValues: (frameId: number, variablesReference: number) =>
    unwrapNullable(DebugServiceBindings.GetInlineValues(frameId, variablesReference), []),
  startSession: (cfg: {
    name?: string;
    kind: string;
    dir: string;
    program?: string;
    runRegex?: string;
    args?: string[];
    env?: Record<string, string>;
    stopOnEntry?: boolean;
    mode?: string;
  }) =>
    DebugServiceBindings.StartSession({
      name: cfg.name ?? "",
      kind: cfg.kind,
      dir: cfg.dir,
      program: cfg.program ?? "",
      runRegex: cfg.runRegex ?? "",
      args: cfg.args ?? [],
      env: cfg.env ?? {},
      stopOnEntry: !!cfg.stopOnEntry,
      mode: cfg.mode ?? "",
    }),
  stopSession: (sessionID: string) =>
    DebugServiceBindings.StopSession(sessionID),
  getActiveSession: () =>
    DebugServiceBindings.GetActiveSession(),
  setActiveSession: (sessionID: string) =>
    DebugServiceBindings.SetActiveSession(sessionID),
  listSessions: () =>
    unwrapNullable(DebugServiceBindings.ListSessions(), []),
  // F-5 (task-1.md): Data breakpoints
  dataBreakpointInfo: (variablesReference: number, name: string) =>
    unwrapNullable(DebugServiceBindings.DataBreakpointInfo(variablesReference, name), [])
      .then((items) => items.map(fromBindingDataBreakpointInfo)),
  setDataBreakpoints: (breakpoints: DataBreakpoint[]) =>
    DebugServiceBindings.SetDataBreakpoints(breakpoints),
  // F-7 (task-1.md): Debug auxiliary capabilities
  exceptionInfo: (threadId: number) =>
    DebugServiceBindings.ExceptionInfo(threadId)
      .then((info) => info ? fromBindingExceptionInfo(info) : null),
  loadedSources: () =>
    unwrapNullable(DebugServiceBindings.LoadedSources(), []),
  modules: () =>
    unwrapNullable(DebugServiceBindings.Modules(), []),
  completions: (frameId: number, text: string, column: number) =>
    unwrapNullable(DebugServiceBindings.Completions(frameId, text, column), []),
  stepInTargets: (frameId: number) =>
    unwrapNullable(DebugServiceBindings.StepInTargets(frameId), []),
  // GOAL-P1-03: stop-aware step-in targets. The bare stepInTargets above
  // returns a list that cannot be validated later; this variant carries the
  // stop sequence the list belongs to so a stale selection is detectable.
  stepInTargetsForStop: async (frameId: number): Promise<StepInTargetSet> => {
    const set = await DebugServiceBindings.StepInTargetsForStop(frameId);
    return { ...set, targets: set.targets ?? [] };
  },
  stepInWithTarget: (targetId: number, stopSequence: number) =>
    DebugServiceBindings.StepInWithTarget(targetId, stopSequence) as Promise<void>,
  currentStopSequence: () =>
    DebugServiceBindings.CurrentStopSequence() as Promise<number>,
  breakpointLocations: (uri: string, startLine: number, endLine: number) =>
    unwrapNullable(DebugServiceBindings.BreakpointLocations(uri, startLine, endLine), []),
};

export const eslintService = {
  status: async () => {
    const status = await requireNonNull(
      EslintServiceBindings.Status(),
      "EslintService.Status",
    );
    if (
      typeof status.eslint !== "boolean"
      || typeof status.eslint_d !== "boolean"
      || typeof status.useDaemon !== "boolean"
      || typeof status.workspace !== "string"
      || typeof status.hint !== "string"
    ) {
      throw new Error("EslintService.Status returned an invalid result");
    }
    return {
      eslint: status.eslint,
      eslint_d: status.eslint_d,
      useDaemon: status.useDaemon,
      workspace: status.workspace,
      hint: status.hint,
    };
  },
  lintFile: async (filePath: string, content: string, contentHash: string) => {
    const result = await EslintServiceBindings.LintFile(filePath, content, contentHash);
    return {
      ...result,
      diagnostics: result.diagnostics ?? [],
    };
  },
  warmDaemon: () => EslintServiceBindings.WarmDaemon() as Promise<string>,
};

export const coverageService = {
  parseCoverProfile: (profilePath: string) =>
    unwrapNullable(CoverageServiceBindings.ParseCoverProfile(profilePath), []),
  runPackageCoverage: async (packageDir: string) => {
    const result = await CoverageServiceBindings.RunPackageCoverage(packageDir);
    return {
      ...result,
      hits: result.hits ?? [],
    };
  },
  parseIstanbulCoverage: (reportPath: string) =>
    CoverageServiceBindings.ParseIstanbulCoverage(reportPath).then((report) => ({
      ...report,
      files: (report.files ?? []).map((file) => ({
        ...file,
        hits: file.hits ?? [],
      })),
    })),
  buildVitestCoverageCommand: (workspaceRoot: string) =>
    CoverageServiceBindings.BuildVitestCoverageCommand(workspaceRoot).then((command) => ({
      ...command,
      args: command.args ?? [],
    })),
  runVitestCoverage: (workspaceRoot: string, timeoutSeconds = 300, signal?: AbortSignal) => {
    const request = CoverageServiceBindings.RunVitestCoverage(workspaceRoot, timeoutSeconds);
    if (signal) request.cancelOn(signal);
    return request.then((result) => ({
      ...result,
      command: {
        ...result.command,
        args: result.command.args ?? [],
      },
      report: {
        ...result.report,
        files: (result.report.files ?? []).map((file) => ({
          ...file,
          hits: file.hits ?? [],
        })),
      },
    }));
  },
};
