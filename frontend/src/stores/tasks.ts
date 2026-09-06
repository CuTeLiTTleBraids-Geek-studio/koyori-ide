// Task store: loads project-scoped task definitions from .koyori-ide/tasks.json
// (via the backend TaskService) and runs them in a new terminal session.
// Koyori IDE 模块 · Tasks；交互服务：任务（TaskService）。
// 喵，这是 Koyori IDE 的 Tasks 模块（前端实现）~
import { reactive, computed } from "vue";
import { taskService } from "@/api/services";
import { appState } from "@/stores/app";
import { pushOutput } from "@/stores/output";
import { notifyError } from "@/lib/notifications";
import { errorMessage } from "@/lib/errors";
import type { TaskDef } from "@/types";

interface TaskStoreState {
  tasks: TaskDef[];
  loading: boolean;
  errorMessage: string | null;
}

export const taskState = reactive<TaskStoreState>({
  tasks: [],
  loading: false,
  errorMessage: null,
});

export const hasTasks = computed(() => taskState.tasks.length > 0);

let taskRuntimeGeneration = 0;
let taskLoadRequest = 0;
let taskExecutionSequence = 0;
const activeTaskExecutions = new Set<string>();

export function cleanupTaskStoreTimers(): void {
  taskRuntimeGeneration += 1;
  taskLoadRequest += 1;
  for (const executionId of activeTaskExecutions) {
    void taskService.stop(executionId).catch(() => undefined);
  }
}

// Load the tasks file for the given project root. Safe to call repeatedly;
// a no-op when root is empty. Errors are surfaced to the store and a
// notification, but do not throw.
export async function loadTasks(
  projectRoot: string,
  shouldApply: () => boolean = () => true,
): Promise<void> {
  const requestId = ++taskLoadRequest;
  const runtimeGeneration = taskRuntimeGeneration;
  const isCurrent = (): boolean =>
    requestId === taskLoadRequest &&
    runtimeGeneration === taskRuntimeGeneration &&
    shouldApply();
  if (!projectRoot) {
    if (isCurrent()) {
      taskState.tasks = [];
      taskState.errorMessage = null;
      taskState.loading = false;
    }
    return;
  }
  if (!isCurrent()) return;
  taskState.loading = true;
  taskState.errorMessage = null;
  try {
    const tasks = await taskService.loadTasks(projectRoot);
    if (!isCurrent()) return;
    taskState.tasks = tasks;
  } catch (e: unknown) {
    if (!isCurrent()) return;
    taskState.errorMessage = errorMessage(e);
    notifyError(`Failed to load tasks: ${taskState.errorMessage}`);
  } finally {
    if (isCurrent()) taskState.loading = false;
  }
}

// composeCommandLine mirrors the Go TaskDef.ComposeCommandLine so the
// frontend can build the shell command without a round-trip. Args are
// single-quoted with embedded-quote escaping.
export function composeCommandLine(task: TaskDef): string {
  let out = task.command;
  for (const a of task.args ?? []) {
    out += " " + shellQuote(a);
  }
  return out;
}

function shellQuote(s: string): string {
  // Replace each ' with '\'' (close quote, escaped backslash-quote, reopen).
  // In the template literal, \\ produces a single backslash, and \' produces
  // a single quote (since \' is not a recognized escape, the \ is dropped).
  return "'" + s.replace(/'/g, `'\\''`) + "'";
}

// resolveCwd returns the directory the task should run in. Absolute task
// cwd values are joined to the project root for safety (no escape).
export function resolveCwd(task: TaskDef, projectRoot: string): string {
  if (!task.cwd) return projectRoot;
  // Normalize: if the cwd is absolute, use it; otherwise join to root.
  if (/^[A-Za-z]:[\\/]/.test(task.cwd) || task.cwd.startsWith("/")) {
    return task.cwd;
  }
  const root = projectRoot.replace(/[\\/]+$/, "");
  return root + "/" + task.cwd;
}

function nextTaskExecutionId(label: string): string {
  taskExecutionSequence += 1;
  const safeLabel = label.replace(/[^A-Za-z0-9_-]+/g, "-").slice(0, 48) || "task";
  return `task:${safeLabel}:${Date.now().toString(36)}:${taskExecutionSequence}`;
}

// runTask uses TaskService's backend approval facade. TaskService redeems the
// authoritative Agent `run` capability and keeps the process cancellable by
// execution ID; the renderer never writes a command directly to a PTY.
export async function runTask(task: TaskDef, projectRoot: string): Promise<void> {
  const cwd = resolveCwd(task, projectRoot);
  const cmd = composeCommandLine(task);
  const runtimeGeneration = taskRuntimeGeneration;
  const workspaceGeneration = Number.isSafeInteger(appState.workspaceGeneration)
    ? appState.workspaceGeneration
    : null;
  const authorityRoot = appState.workspaceRoot || appState.currentProject || "";
  const authorityCurrent = (): boolean =>
    runtimeGeneration === taskRuntimeGeneration &&
    (workspaceGeneration === null || appState.workspaceGeneration === workspaceGeneration) &&
    (!authorityRoot || normalizeTaskPath(appState.workspaceRoot || appState.currentProject || "") === normalizeTaskPath(authorityRoot));
  if (authorityRoot && normalizeTaskPath(projectRoot) !== normalizeTaskPath(authorityRoot)) {
    notifyError(`Task "${task.label}" belongs to a stale workspace`);
    return;
  }
  const executionId = nextTaskExecutionId(task.label);
  activeTaskExecutions.add(executionId);
  try {
    const approvalToken = await taskService.requestExecutionApproval(executionId, cmd, cwd);
    if (!authorityCurrent()) return;
    pushOutput("task", "info", `Running task "${task.label}": ${cmd}`);
    const result = await taskService.executeApproved(executionId, cmd, cwd, approvalToken);
    if (!authorityCurrent()) return;
    if (result.stdout) pushOutput("task", "info", result.stdout);
    if (result.stderr) pushOutput("task", result.exitCode === 0 ? "warn" : "error", result.stderr);
    if (result.blocked || result.exitCode !== 0) {
      const reason = result.blockReason ?? `exit code ${result.exitCode}`;
      notifyError(`Failed to run task "${task.label}": ${reason}`);
    }
  } catch (e: unknown) {
    notifyError(`Failed to run task "${task.label}": ${errorMessage(e)}`);
  } finally {
    activeTaskExecutions.delete(executionId);
  }
}

function normalizeTaskPath(path: string): string {
  const normalized = path.replace(/\\/g, "/").replace(/\/+/g, "/").replace(/\/$/, "");
  // POSIX paths are case-sensitive. Fold only Windows drive/UNC syntax so a
  // case-distinct POSIX workspace cannot be treated as the same authority.
  const windowsPath = /^[A-Za-z]:\//.test(normalized) || normalized.startsWith("//");
  return windowsPath ? normalized.toLowerCase() : normalized;
}

import.meta.hot?.dispose(cleanupTaskStoreTimers);
