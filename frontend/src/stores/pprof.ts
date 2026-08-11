/**
 * Priority 7 (prompt-1.md 422-432): Go pprof 性能分析 store。
 * 提供 CPU / Heap / Goroutine 采样的前端状态管理与后端调用封装，
 * 供 ProfilePanel 使用。
 */
// Koyori IDE 模块 · Pprof；交互服务：文件系统（FileService）、性能分析（PProfService）。
// 喵，这是 Koyori IDE 的 Pprof 模块（前端实现）~
import { reactive } from "vue";
import { pprofService, fileService } from "@/api/services";
import { appState } from "@/stores/app";
import { pushOutput } from "@/stores/output";
import { notifyError, notifySuccess } from "@/lib/notifications";
import type { ProfileAnalysis } from "@/types";

export type ProfileKind = "cpu" | "heap" | "goroutine" | "block" | "mutex" | "trace";
export type TraceProfileView = "net" | "sync" | "syscall" | "sched";

export interface PProfState {
  /** CPU 采样是否进行中。 */
  cpuProfiling: boolean;
  /** 任意采样/分析操作进行中。 */
  loading: boolean;
  /** 最近一次分析结果。 */
  analysis: ProfileAnalysis | null;
  /** 最近一次生成的 profile 文件路径。 */
  lastProfilePath: string;
  /** 最近一次操作错误（用于 UI 提示）。 */
  lastError: string;
  /** 当前分析结果对应的 profile 类型。 */
  lastKind: ProfileKind | "";
  /** 当前 CPU 采样输出路径（停止后用于自动分析）。 */
  cpuOutputPath: string;
  activeProfile: ProfileKind | "";
  activeOutputPath: string;
}

export const pprofState = reactive<PProfState>({
  cpuProfiling: false,
  loading: false,
  analysis: null,
  lastProfilePath: "",
  lastError: "",
  lastKind: "",
  cpuOutputPath: "",
  activeProfile: "",
  activeOutputPath: "",
});

const activeSessionStorageKey = "koyori-ide.pprof.active-session";

function isProfileKind(value: unknown): value is ProfileKind {
  return value === "cpu" || value === "heap" || value === "goroutine" || value === "block" || value === "mutex" || value === "trace";
}

function readActiveSession(): { kind: ProfileKind; path: string } | null {
  try {
    const value = JSON.parse(localStorage.getItem(activeSessionStorageKey) ?? "null") as unknown;
    if (!value || typeof value !== "object") return null;
    const session = value as { kind?: unknown; path?: unknown };
    return isProfileKind(session.kind) && typeof session.path === "string"
      ? { kind: session.kind, path: session.path }
      : null;
  } catch {
    return null;
  }
}

function writeActiveSession(kind: ProfileKind | "", path = ""): void {
  try {
    if (!kind) localStorage.removeItem(activeSessionStorageKey);
    else localStorage.setItem(activeSessionStorageKey, JSON.stringify({ kind, path }));
  } catch {
    // Profiling remains usable when browser storage is unavailable.
  }
}

/**
 * 生成默认 profile 输出路径：<project>/.pprof/<kind>-<timestamp>.prof。
 * 无打开项目时返回空字符串（调用方应提示用户先打开项目）。
 */
export function defaultProfilePath(kind: ProfileKind): string {
  const root = appState.currentProject || "";
  if (!root) return "";
  const ts = new Date().toISOString().replace(/[:.]/g, "-");
  const base = root.replace(/[\\/]+$/, "");
  const extension = kind === "trace" ? "trace" : "prof";
  return `${base}/.pprof/${kind}-${ts}.${extension}`;
}

/** 确保输出文件的父目录存在（best-effort）。 */
async function ensureParentDir(path: string): Promise<void> {
  if (!path) return;
  const slash = Math.max(path.lastIndexOf("/"), path.lastIndexOf("\\"));
  if (slash <= 0) return;
  const dir = path.slice(0, slash);
  try {
    await fileService.createDirectory(dir);
  } catch {
    // 忽略：目录已存在或创建失败时由后端 os.Create 报错。
  }
}

/** 刷新 CPU 采样状态。 */
export async function refreshProfilingStatus(): Promise<void> {
  try {
    pprofState.cpuProfiling = await pprofService.isProfiling();
    const active = await pprofService.activeProfile();
    if (!isProfileKind(active)) {
      pprofState.activeProfile = "";
      pprofState.activeOutputPath = "";
      writeActiveSession("");
      return;
    }
    const stored = readActiveSession();
    const currentPath = pprofState.activeProfile === active ? pprofState.activeOutputPath : "";
    const storedPath = stored?.kind === active ? stored.path : "";
    const fallbackPath = active === "block" || active === "mutex" ? defaultProfilePath(active) : "";
    const recoveredPath = currentPath || storedPath || fallbackPath;
    pprofState.activeProfile = active;
    pprofState.activeOutputPath = recoveredPath;
    if (active === "cpu") pprofState.cpuOutputPath = recoveredPath;
    if (recoveredPath) writeActiveSession(active, recoveredPath);
  } catch {
    pprofState.cpuProfiling = false;
  }
}

/**
 * 开始 CPU 采样。outputPath 省略时自动生成到 <project>/.pprof/。
 * 返回是否成功。
 */
export async function startCPUProfile(outputPath?: string): Promise<boolean> {
  const out = outputPath || defaultProfilePath("cpu");
  if (!out) {
    notifyError("Open a project first to generate a profile path");
    return false;
  }
  pprofState.loading = true;
  try {
    await ensureParentDir(out);
    await pprofService.startCPUProfile(out);
    pprofState.cpuProfiling = true;
    pprofState.activeProfile = "cpu";
    pprofState.activeOutputPath = out;
    pprofState.cpuOutputPath = out;
    pprofState.lastKind = "cpu";
    pprofState.lastProfilePath = out;
    pprofState.lastError = "";
    writeActiveSession("cpu", out);
    notifySuccess("CPU profiling started");
    return true;
  } catch (e) {
    pprofState.lastError = errText(e);
    notifyError(pprofState.lastError);
    return false;
  } finally {
    pprofState.loading = false;
  }
}

/**
 * 停止 CPU 采样。analyze 为 true（默认）时自动分析刚生成的 profile。
 */
export async function stopCPUProfile(analyze = true): Promise<void> {
  pprofState.loading = true;
  try {
    await pprofService.stopCPUProfile();
    pprofState.cpuProfiling = false;
    pprofState.activeProfile = "";
    pprofState.activeOutputPath = "";
    const out = pprofState.cpuOutputPath;
    pprofState.cpuOutputPath = "";
    writeActiveSession("");
    pushOutput("Profile", "info", `CPU profile saved: ${out}`);
    notifySuccess("CPU profiling stopped");
    if (analyze && out) {
      await analyzeProfile(out);
    }
  } catch (e) {
    pprofState.lastError = errText(e);
    notifyError(pprofState.lastError);
  } finally {
    pprofState.loading = false;
  }
}

/** 抓取堆 profile。 */
export async function captureHeapProfile(outputPath?: string): Promise<void> {
  const out = outputPath || defaultProfilePath("heap");
  if (!out) {
    notifyError("Open a project first to generate a profile path");
    return;
  }
  pprofState.loading = true;
  try {
    await ensureParentDir(out);
    await pprofService.captureHeapProfile(out);
    pprofState.lastProfilePath = out;
    pprofState.lastKind = "heap";
    pprofState.lastError = "";
    pushOutput("Profile", "info", `Heap profile saved: ${out}`);
    notifySuccess("Heap profile captured");
    await analyzeProfile(out);
  } catch (e) {
    pprofState.lastError = errText(e);
    notifyError(pprofState.lastError);
  } finally {
    pprofState.loading = false;
  }
}

/** 抓取 goroutine profile。 */
export async function captureGoroutineProfile(outputPath?: string): Promise<void> {
  const out = outputPath || defaultProfilePath("goroutine");
  if (!out) {
    notifyError("Open a project first to generate a profile path");
    return;
  }
  pprofState.loading = true;
  try {
    await ensureParentDir(out);
    // debug=0 输出二进制 pprof 格式，可供 analyzeProfile 解析。
    await pprofService.captureGoroutineProfile(out, 0);
    pprofState.lastProfilePath = out;
    pprofState.lastKind = "goroutine";
    pprofState.lastError = "";
    pushOutput("Profile", "info", `Goroutine profile saved: ${out}`);
    notifySuccess("Goroutine profile captured");
    await analyzeProfile(out);
  } catch (e) {
    pprofState.lastError = errText(e);
    notifyError(pprofState.lastError);
  } finally {
    pprofState.loading = false;
  }
}

async function startSampleProfile(
  kind: "block" | "mutex",
  outputPath: string | undefined,
  start: () => Promise<void>,
): Promise<boolean> {
  const out = outputPath || defaultProfilePath(kind);
  if (!out) {
    notifyError("Open a project first to generate a profile path");
    return false;
  }
  pprofState.loading = true;
  try {
    await ensureParentDir(out);
    await start();
    pprofState.activeProfile = kind;
    pprofState.activeOutputPath = out;
    pprofState.lastKind = kind;
    pprofState.lastProfilePath = out;
    pprofState.lastError = "";
    writeActiveSession(kind, out);
    notifySuccess(`${kind} profiling started`);
    return true;
  } catch (e) {
    pprofState.lastError = errText(e);
    notifyError(pprofState.lastError);
    return false;
  } finally {
    pprofState.loading = false;
  }
}

async function stopSampleProfile(
  kind: "block" | "mutex",
  stop: (outputPath: string) => Promise<void>,
  analyze = true,
): Promise<void> {
  const out = pprofState.activeOutputPath;
  pprofState.loading = true;
  let stopped = false;
  try {
    await stop(out);
    stopped = true;
    pushOutput("Profile", "info", `${kind} profile saved: ${out}`);
    notifySuccess(`${kind} profiling stopped`);
    if (analyze && out) await analyzeProfile(out);
  } catch (e) {
    pprofState.lastError = errText(e);
    notifyError(pprofState.lastError);
    await refreshProfilingStatus();
  } finally {
    if (stopped) {
      pprofState.activeProfile = "";
      pprofState.activeOutputPath = "";
      writeActiveSession("");
    }
    pprofState.loading = false;
  }
}

export function startBlockProfile(outputPath?: string): Promise<boolean> {
  return startSampleProfile("block", outputPath, () => pprofService.startBlockProfile());
}

export function stopBlockProfile(analyze = true): Promise<void> {
  return stopSampleProfile("block", (path) => pprofService.stopBlockProfile(path), analyze);
}

export function startMutexProfile(outputPath?: string): Promise<boolean> {
  return startSampleProfile("mutex", outputPath, () => pprofService.startMutexProfile());
}

export function stopMutexProfile(analyze = true): Promise<void> {
  return stopSampleProfile("mutex", (path) => pprofService.stopMutexProfile(path), analyze);
}

export async function startTrace(outputPath?: string): Promise<boolean> {
  const out = outputPath || defaultProfilePath("trace");
  if (!out) {
    notifyError("Open a project first to generate a profile path");
    return false;
  }
  pprofState.loading = true;
  try {
    await ensureParentDir(out);
    await pprofService.startTrace(out);
    pprofState.activeProfile = "trace";
    pprofState.activeOutputPath = out;
    pprofState.lastKind = "trace";
    pprofState.lastProfilePath = out;
    pprofState.lastError = "";
    writeActiveSession("trace", out);
    notifySuccess("runtime trace started");
    return true;
  } catch (e) {
    pprofState.lastError = errText(e);
    notifyError(pprofState.lastError);
    return false;
  } finally {
    pprofState.loading = false;
  }
}

export async function stopTrace(): Promise<void> {
  const out = pprofState.activeOutputPath;
  pprofState.loading = true;
  let stopped = false;
  try {
    await pprofService.stopTrace();
    stopped = true;
    pushOutput("Profile", "info", `Runtime trace saved: ${out}`);
    notifySuccess("runtime trace stopped");
  } catch (e) {
    pprofState.lastError = errText(e);
    notifyError(pprofState.lastError);
    await refreshProfilingStatus();
  } finally {
    if (stopped) {
      pprofState.activeProfile = "";
      pprofState.activeOutputPath = "";
      writeActiveSession("");
    }
    pprofState.loading = false;
  }
}

export async function analyzeTrace(tracePath: string, view: TraceProfileView): Promise<ProfileAnalysis | null> {
  if (!tracePath) return null;
  pprofState.loading = true;
  try {
    const result = await pprofService.analyzeTrace(tracePath, view);
    pprofState.analysis = result;
    pprofState.lastProfilePath = tracePath;
    return result;
  } catch (e) {
    pprofState.lastError = errText(e);
    pprofState.analysis = null;
    notifyError(pprofState.lastError);
    return null;
  } finally {
    pprofState.loading = false;
  }
}

/** 分析一个 profile 文件并把结果写入 pprofState.analysis。 */
export async function analyzeProfile(profilePath: string): Promise<ProfileAnalysis | null> {
  if (!profilePath) return null;
  pprofState.loading = true;
  try {
    const result = await pprofService.analyzeProfile(profilePath);
    pprofState.analysis = result;
    pprofState.lastProfilePath = profilePath;
    return result;
  } catch (e) {
    pprofState.lastError = errText(e);
    pprofState.analysis = null;
    notifyError(pprofState.lastError);
    return null;
  } finally {
    pprofState.loading = false;
  }
}

/** 清除当前分析结果。 */
export function clearAnalysis(): void {
  pprofState.analysis = null;
  pprofState.lastError = "";
  pprofState.lastKind = "";
}

/**
 * 把纳秒数值格式化为人类可读时长（ns/µs/ms/s）。
 * 纯函数，便于测试与 UI 复用。
 */
export function formatDuration(ns: number): string {
  if (!ns) return "0";
  const abs = Math.abs(ns);
  if (abs < 1_000) return `${ns} ns`;
  if (abs < 1_000_000) return `${(ns / 1_000).toFixed(2)} µs`;
  if (abs < 1_000_000_000) return `${(ns / 1_000_000).toFixed(2)} ms`;
  return `${(ns / 1_000_000_000).toFixed(2)} s`;
}

/** 把字节数格式化为人类可读大小（用于 heap profile 的 bytes 单位）。 */
export function formatBytes(bytes: number): string {
  if (!bytes || bytes <= 0) return "0 B";
  const abs = Math.abs(bytes);
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  let val = abs;
  while (val >= 1024 && i < units.length - 1) {
    val /= 1024;
    i++;
  }
  return `${val.toFixed(2)} ${units[i]}`;
}

function errText(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}
