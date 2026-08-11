// Koyori IDE 模块 · Platform；交互服务：恢复（RecoveryService）、更新（UpdateService）、崩溃报告（CrashService）、数据库（DatabaseService）、性能分析（PProfService）。
// 喵，这是 Koyori IDE 的 Platform 模块（前端实现）~
import * as DatabaseServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/databaseservice.js";
import * as PProfServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/pprofservice.js";
import * as UpdateServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/updateservice.js";
import * as CrashServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/crashservice.js";
import * as RecoveryServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/recoveryservice.js";
import * as RemoteServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/remoteservice.js";
import type {
  CrashReport, DatabaseConnectionConfig, DatabaseQueryRequest,
  DatabaseQueryResult, ProfileAnalysis, SSHConfig,
} from "@/types";
import { requireNonNull, unwrapNullable } from "./boundary";

// BUG2: Use only repository-generated bindings. The generated module owns the
// stable numeric IDs, avoiding hand-written runtime method names and IDs.
export const databaseService = {
  connect: (config: DatabaseConnectionConfig) =>
    DatabaseServiceBindings.Connect(config),
  listConnections: () =>
    unwrapNullable(DatabaseServiceBindings.ListConnections(), []),
  disconnect: (connectionId: string) =>
    DatabaseServiceBindings.Disconnect(connectionId) as Promise<void>,
  listSchemas: (connectionId: string) =>
    unwrapNullable(DatabaseServiceBindings.ListSchemas(connectionId), []),
  listTables: (connectionId: string, schema: string) =>
    unwrapNullable(DatabaseServiceBindings.ListTables(connectionId, schema), []),
  describeTable: (connectionId: string, schema: string, table: string) =>
    unwrapNullable(DatabaseServiceBindings.DescribeTable(connectionId, schema, table), []),
  queryPage: async (request: DatabaseQueryRequest): Promise<DatabaseQueryResult> => {
    const result = await DatabaseServiceBindings.QueryPage(request);
    return {
      ...result,
      columns: result.columns ?? [],
      rows: (result.rows ?? []).map((row) => row ?? []),
    };
  },
  cancelQuery: (requestId: string) =>
    DatabaseServiceBindings.CancelQuery(requestId) as Promise<boolean>,
};

// Priority 7 (prompt-1.md 422-432): Go pprof 性能分析集成。
// PProfService calls use the repository-generated module above.
type BindingProfileAnalysis = NonNullable<
  Awaited<ReturnType<typeof PProfServiceBindings.AnalyzeProfile>>
>;
type BindingFlameGraphNode = NonNullable<BindingProfileAnalysis["flameGraph"]>;

function fromBindingFlameGraphNode(node: BindingFlameGraphNode): NonNullable<
  ProfileAnalysis["flameGraph"]
> {
  return {
    ...node,
    children: (node.children ?? []).map(fromBindingFlameGraphNode),
  };
}

function fromBindingProfileAnalysis(analysis: BindingProfileAnalysis): ProfileAnalysis {
  return {
    ...analysis,
    topFunctions: analysis.topFunctions ?? [],
    flameGraph: analysis.flameGraph
      ? fromBindingFlameGraphNode(analysis.flameGraph)
      : analysis.flameGraph,
  };
}

export const pprofService = {
  startCPUProfile: (outputPath: string) =>
    PProfServiceBindings.StartCPUProfile(outputPath),
  stopCPUProfile: () =>
    PProfServiceBindings.StopCPUProfile(),
  isProfiling: () =>
    PProfServiceBindings.IsProfiling(),
  activeProfile: () =>
    PProfServiceBindings.ActiveProfile(),
  startBlockProfile: () =>
    PProfServiceBindings.StartBlockProfile(),
  stopBlockProfile: (outputPath: string) =>
    PProfServiceBindings.StopBlockProfile(outputPath),
  startMutexProfile: () =>
    PProfServiceBindings.StartMutexProfile(),
  stopMutexProfile: (outputPath: string) =>
    PProfServiceBindings.StopMutexProfile(outputPath),
  startTrace: (outputPath: string) =>
    PProfServiceBindings.StartTrace(outputPath),
  stopTrace: () =>
    PProfServiceBindings.StopTrace(),
  captureHeapProfile: (outputPath: string) =>
    PProfServiceBindings.CaptureHeapProfile(outputPath),
  captureGoroutineProfile: (outputPath: string, debug: number) =>
    PProfServiceBindings.CaptureGoroutineProfile(outputPath, debug),
  analyzeProfile: async (profilePath: string) =>
    fromBindingProfileAnalysis(await requireNonNull(
      PProfServiceBindings.AnalyzeProfile(profilePath),
      "PProfService.AnalyzeProfile",
    )),
  analyzeTrace: async (tracePath: string, view: string) =>
    fromBindingProfileAnalysis(await requireNonNull(
      PProfServiceBindings.AnalyzeTrace(tracePath, view),
      "PProfService.AnalyzeTrace",
    )),
};

// 优先级 10 (prompt-1.md): 自动更新 + 崩溃报告。
// The generated UpdateService module is the only runtime call entry point.
// The service is registered in bootstrap_services.go.
export const updateService = {
  // 查询 GitHub Releases latest 端点。currentVersion 为当前应用版本；
  // updateURL 为空时后端使用默认 owner/repo 端点。
  checkForUpdates: (currentVersion: string, updateURL: string) =>
    requireNonNull(
      UpdateServiceBindings.CheckForUpdates(currentVersion, updateURL),
      "UpdateService.CheckForUpdates",
    ),
  // 语义化版本比较：-1 表示 current < latest，0 相等，1 current > latest。
  compareVersions: (current: string, latest: string) =>
    UpdateServiceBindings.CompareVersions(current, latest),
  // 下载更新包到 destPath（HTTPS + github.com 域名校验）。
  downloadUpdate: (downloadURL: string, destPath: string) =>
    UpdateServiceBindings.DownloadUpdate(downloadURL, destPath),
  // 返回当前应用版本（从构建信息读取，失败返回 "dev"）。
  getCurrentVersion: () =>
    UpdateServiceBindings.GetCurrentVersion(),
};

export const crashService = {
  // 写入一条崩溃报告到崩溃目录。
  reportCrash: (report: CrashReport) =>
    CrashServiceBindings.ReportCrash(report),
  // 列出所有崩溃报告（仅元数据，按时间戳降序）。
  getCrashReports: () =>
    unwrapNullable(CrashServiceBindings.GetCrashReports(), []),
  // 读取指定崩溃报告的完整内容。
  getCrashReport: (filename: string) =>
    requireNonNull(
      CrashServiceBindings.GetCrashReport(filename),
      "CrashService.GetCrashReport",
    ),
  // 删除指定崩溃报告（幂等）。
  deleteCrashReport: (filename: string) =>
    CrashServiceBindings.DeleteCrashReport(filename),
  // 删除所有崩溃报告。
  clearAllCrashReports: () =>
    CrashServiceBindings.ClearAllCrashReports(),
};

// GOAL-P0-03: editor dirty-buffer recovery (hot exit).
//
// Deliberately separate from crashService above: crashService persists panic
// stack traces for diagnostics and is not a content backup, while this service
// persists unsaved buffer content so an abnormal exit does not lose the user's
// work. Recovery never overwrites a newer file on disk — ScanRecoverable
// reports a "conflict" status and the UI must let the user choose.
export const recoveryService = {
  // 记录一个脏缓冲。调用方需先用 computeBaseline 取得基线 mtime/hash。
  // 超出单文件或工作区限额时 reject，UI 必须把该错误显示给用户。
  saveDirtyBuffer: (
    windowId: string,
    path: string,
    content: string,
    encoding: string,
    eol: string,
    baselineMtime: number,
    baselineHash: string,
  ) =>
    RecoveryServiceBindings.SaveDirtyBuffer(
      windowId, path, content, encoding, eol, baselineMtime, baselineHash,
    ),
  // 保存成功或用户明确丢弃后清除单个文件的记录（幂等）。
  clearDirtyBuffer: (windowId: string, path: string) =>
    RecoveryServiceBindings.ClearDirtyBuffer(windowId, path),
  // 正常关闭窗口时清除该窗口的全部记录。
  clearWindowJournal: (windowId: string) =>
    RecoveryServiceBindings.ClearWindowJournal(windowId),
  // 移除工作区时清除该工作区的全部记录。
  clearWorkspaceJournal: () =>
    RecoveryServiceBindings.ClearWorkspaceJournal(),
  // 启动时扫描可恢复内容。逐文件比较基线与当前磁盘 hash 得出 status。
  scanRecoverable: () =>
    requireNonNull(
      RecoveryServiceBindings.ScanRecoverable(),
      "RecoveryService.ScanRecoverable",
    ),
  getRecoveryState: () =>
    requireNonNull(
      RecoveryServiceBindings.GetRecoveryState(),
      "RecoveryService.GetRecoveryState",
    ),
  completeRecovery: (
    decisions: Array<{ windowId: string; path: string }>,
    corruptFiles: string[],
  ) => RecoveryServiceBindings.CompleteRecovery(decisions, corruptFiles),
  acknowledgeRecoveryFailure: () =>
    RecoveryServiceBindings.AcknowledgeRecoveryFailure(),
  // 读取文件当前的 mtime + hash 作为基线。
  computeBaseline: (path: string) =>
    requireNonNull(
      RecoveryServiceBindings.ComputeBaseline(path),
      "RecoveryService.ComputeBaseline",
    ),
  // 用户在恢复提示中选择"全部丢弃"后清除该窗口记录。
  discardRecoveredSession: (windowId: string) =>
    RecoveryServiceBindings.DiscardRecoveredSession(windowId),
  // 用户可关闭 journal（默认开启）。关闭时同时清除现有记录，
  // 以免留下不再维护的陈旧恢复内容。
  setJournalEnabled: (enabled: boolean) =>
    RecoveryServiceBindings.SetJournalEnabled(enabled),
  isJournalEnabled: () =>
    RecoveryServiceBindings.IsJournalEnabled(),
};

// F-9 (prompt-2.md 第 537-586 行): SSH 远程开发服务。
// Calls use the generated RemoteService module; the service is registered in
// bootstrap_services.go.
// 注意：密码和密钥路径绝不记录到日志（后端 Connect 仅记录 host/port/user）。
export const remoteService = {
  // 建立到远程主机的 SSH 连接，并初始化 SFTP 子系统。
  // name 是会话标识（通常是 host 或用户自定义名称）。
  connect: (name: string, config: SSHConfig) =>
    RemoteServiceBindings.Connect(name, config),
  // 关闭指定会话。未连接的会话返回 nil（幂等）。
  disconnect: (name: string) =>
    RemoteServiceBindings.Disconnect(name),
  // 报告指定会话是否已连接。
  isConnected: (name: string) =>
    RemoteServiceBindings.IsConnected(name),
  requestCommandApproval: (name: string, argv: string[]) =>
    RemoteServiceBindings.RequestCommandApproval(name, argv),
  // 仅携带后端签发且绑定当前 session+argv 的 single-use token 执行。
  executeCommand: (name: string, argv: string[], approvalToken: string) =>
    RemoteServiceBindings.ExecuteCommand(name, argv, approvalToken),
  // 列出所有已连接会话的名称（按字母序）。
  listConnections: () =>
    unwrapNullable(RemoteServiceBindings.ListConnections(), []),
};
