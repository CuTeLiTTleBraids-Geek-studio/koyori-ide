// Task store: loads project-scoped task definitions from .koyori-ide/tasks.json
// (via the backend TaskService) and runs them in a new terminal session.
// Koyori IDE 模块 · Tasks；交互服务：任务（TaskService）。
// 喵，这是 Koyori IDE 的 Tasks 模块（前端实现）~
import { reactive, computed } from "vue";
import { taskService } from "@/api/services";
import { appState } from "@/stores/app";
import { createSession, writeToSession } from "@/stores/terminal";
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

const pendingCommandTimers = new Set<ReturnType<typeof setTimeout>>();
let taskRuntimeGeneration = 0;

export function cleanupTaskStoreTimers(): void {
  taskRuntimeGeneration += 1;
  for (const timer of pendingCommandTimers) {
    clearTimeout(timer);
  }
  pendingCommandTimers.clear();
}

// Load the tasks file for the given project root. Safe to call repeatedly;
// a no-op when root is empty. Errors are surfaced to the store and a
// notification, but do not throw.
export async function loadTasks(projectRoot: string): Promise<void> {
  if (!projectRoot) {
    taskState.tasks = [];
    taskState.errorMessage = null;
    return;
  }
  taskState.loading = true;
  taskState.errorMessage = null;
  try {
    taskState.tasks = await taskService.loadTasks(projectRoot);
  } catch (e: unknown) {
    taskState.errorMessage = errorMessage(e);
    notifyError(`Failed to load tasks: ${taskState.errorMessage}`);
  } finally {
    taskState.loading = false;
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

// runTask creates a new terminal session at the task's cwd, writes the
// command, and switches to the terminal view. The terminal panel must
// already be mounted; the caller is responsible for making it visible.
export async function runTask(task: TaskDef, projectRoot: string): Promise<void> {
  const cwd = resolveCwd(task, projectRoot);
  const cmd = composeCommandLine(task);
  const runtimeGeneration = taskRuntimeGeneration;
  appState.terminalVisible = true;
  try {
    const id = await createSession(cwd);
    if (!id) {
      notifyError(`Failed to start terminal for task "${task.label}"`);
      return;
    }
    if (runtimeGeneration !== taskRuntimeGeneration) return;
    // Small delay to let the shell prompt render before writing the command.
    // The terminal backend doesn't expose a "ready" signal, so this is a
    // pragmatic workaround. 80ms is well below human perception.
    const timer = setTimeout(() => {
      pendingCommandTimers.delete(timer);
      void writeToSession(id, cmd + "\n");
    }, 80);
    pendingCommandTimers.add(timer);
    pushOutput("task", "info", `Running task "${task.label}": ${cmd}`);
  } catch (e: unknown) {
    notifyError(`Failed to run task "${task.label}": ${errorMessage(e)}`);
  }
}

import.meta.hot?.dispose(cleanupTaskStoreTimers);
