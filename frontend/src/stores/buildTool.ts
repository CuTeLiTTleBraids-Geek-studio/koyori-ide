// Koyori IDE 模块 · Build Tool；交互服务：任务（TaskService）、文件系统（FileService）、工具链（ToolchainService）。
// 喵，这是 Koyori IDE 的 Build Tool 模块（前端实现）~
import { reactive } from "vue";
import { fileService, taskService, toolchainService } from "@/api/services";
import { composeCommandLine, resolveCwd } from "@/stores/tasks";
import {
  createSession,
  killSession,
  onTerminalOutput,
  runCommandInSessionCapturing,
} from "@/stores/terminal";
import {
  debugState,
  launchWithConfig,
  loadLaunchConfigs,
  stopDebugSession,
  type DebugLaunchConfig,
} from "@/stores/debug";
import { clearProblems, pushOutput, pushProblem } from "@/stores/output";
import { parseToolOutputToProblems } from "@/lib/toolOutputProblems";
import { notifyError, notifyWarning } from "@/lib/notifications";
import type { TaskDef, ToolchainCommand } from "@/types";

export type BuildTaskSource = "task" | "launch" | "toolchain" | "npm" | "make" | "taskfile";
export type BuildRunStatus = "idle" | "running" | "success" | "failed" | "cancelled";

export interface BuildTaskItem {
  id: string;
  label: string;
  source: BuildTaskSource;
  description?: string;
  task?: TaskDef;
  launch?: DebugLaunchConfig;
  language?: ToolchainCommand["language"];
}

export interface BuildRun {
  taskId: string;
  status: BuildRunStatus;
  output: string;
  durationMs: number;
  startedAt: number;
  sessionId: string | null;
}

interface BuildToolState {
  workspaceRoot: string;
  tasks: BuildTaskItem[];
  loading: boolean;
  errorMessage: string | null;
  favorites: string[];
  recent: string[];
  runs: Record<string, BuildRun>;
  activeTaskId: string | null;
  selectedTaskId: string | null;
}

export const buildToolState = reactive<BuildToolState>({
  workspaceRoot: "",
  tasks: [],
  loading: false,
  errorMessage: null,
  favorites: [],
  recent: [],
  runs: {},
  activeTaskId: null,
  selectedTaskId: null,
});

let discoveryGeneration = 0;
let runGeneration = 0;

const SAFE_TARGET = /^[A-Za-z0-9][A-Za-z0-9_.:/-]*$/;
const SAFE_COMMAND_TOKEN = /^[A-Za-z0-9_@./:+\\-]+$/;
const FAVORITES_KEY = "koyori-ide.build.favorites:";
const RECENT_KEY = "koyori-ide.build.recent:";
const RECENT_LIMIT = 12;

function workspacePath(root: string, relative: string): string {
  return `${root.replace(/[\\/]+$/, "")}/${relative}`;
}

function makeTerminalItem(
  source: Exclude<BuildTaskSource, "launch">,
  id: string,
  label: string,
  command: string,
  args: string[] = [],
  description = "",
): BuildTaskItem {
  return {
    id: `${source}:${id}`,
    label,
    source,
    description,
    task: { label, command, args },
  };
}

export function parsePackageScripts(raw: string): BuildTaskItem[] {
  try {
    const parsed = JSON.parse(raw) as { scripts?: Record<string, unknown> };
    return Object.entries(parsed.scripts ?? {})
      .filter(([name, value]) => SAFE_TARGET.test(name) && typeof value === "string")
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([name, command]) => makeTerminalItem(
        "npm",
        name,
        `npm: ${name}`,
        "npm",
        ["run", name],
        command as string,
      ));
  } catch {
    return [];
  }
}

export function parseMakeTargets(raw: string): string[] {
  const targets = new Set<string>();
  for (const line of raw.split(/\r?\n/)) {
    if (!line || /^\s/.test(line) || line.startsWith("#")) continue;
    const colon = line.indexOf(":");
    if (colon <= 0 || line[colon + 1] === "=") continue;
    for (const target of line.slice(0, colon).trim().split(/\s+/)) {
      if (SAFE_TARGET.test(target) && !target.startsWith(".") && !target.includes("%")) {
        targets.add(target);
      }
    }
  }
  return [...targets];
}

export function parseTaskfileTargets(raw: string): string[] {
  const targets: string[] = [];
  let inTasks = false;
  for (const line of raw.split(/\r?\n/)) {
    if (/^tasks:\s*(?:#.*)?$/.test(line)) {
      inTasks = true;
      continue;
    }
    if (!inTasks) continue;
    if (/^[^\s#]/.test(line)) break;
    const match = line.match(/^ {2}([A-Za-z0-9][A-Za-z0-9_.:/-]*):(?:\s|$)/);
    const name = match?.[1]?.trim() ?? "";
    if (SAFE_TARGET.test(name)) targets.push(name);
  }
  return targets;
}

function taskSignature(item: BuildTaskItem): string {
  if (item.launch) return `launch:${item.launch.name}`;
  const task = item.task;
  if (!task) return item.id;
  return JSON.stringify([task.command, ...(task.args ?? []), task.cwd ?? ""]);
}

function sourcePriority(source: BuildTaskSource): number {
  if (source === "task" || source === "launch") return 0;
  if (source === "toolchain") return 1;
  return 2;
}

export function dedupeBuildTasks(items: BuildTaskItem[]): BuildTaskItem[] {
  const bySignature = new Map<string, BuildTaskItem>();
  const order: string[] = [];
  for (const item of items) {
    const signature = taskSignature(item);
    const existing = bySignature.get(signature);
    if (!existing) {
      bySignature.set(signature, item);
      order.push(signature);
    } else if (sourcePriority(item.source) < sourcePriority(existing.source)) {
      bySignature.set(signature, item);
    }
  }
  return order.map((signature) => bySignature.get(signature)!);
}

function standardTaskItems(tasks: TaskDef[]): BuildTaskItem[] {
  return tasks.map((task) => ({
    id: `task:${task.label}`,
    label: task.label,
    source: "task" as const,
    description: task.group,
    task,
  }));
}

function launchItems(configs: DebugLaunchConfig[]): BuildTaskItem[] {
  return configs.map((launch) => ({
    id: `launch:${launch.name}`,
    label: launch.name,
    source: "launch" as const,
    description: launch.request === "attach" ? "Attach" : "Launch",
    launch,
  }));
}

function toolchainItem(command: ToolchainCommand): BuildTaskItem | null {
  const tokens = command.command.trim().split(/\s+/).filter(Boolean);
  if (tokens.length === 0 || tokens.some((token) => !SAFE_COMMAND_TOKEN.test(token))) return null;
  return {
    id: `toolchain:${command.id}`,
    label: command.label,
    source: "toolchain",
    description: command.description,
    language: command.language,
    task: {
      label: command.label,
      command: tokens[0],
      args: [...tokens.slice(1), ...(command.args ?? [])],
    },
  };
}

async function readOptional(path: string): Promise<string> {
  try {
    return await fileService.readFile(path);
  } catch {
    return "";
  }
}

function loadStoredList(prefix: string, root: string): string[] {
  try {
    const value = JSON.parse(localStorage.getItem(prefix + root) ?? "[]") as unknown;
    return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
  } catch {
    return [];
  }
}

function saveStoredList(prefix: string, values: string[]): void {
  if (!buildToolState.workspaceRoot) return;
  localStorage.setItem(prefix + buildToolState.workspaceRoot, JSON.stringify(values));
}

export async function refreshBuildTasks(root: string): Promise<void> {
  const generation = ++discoveryGeneration;
  if (!root) {
    buildToolState.workspaceRoot = "";
    buildToolState.tasks = [];
    buildToolState.favorites = [];
    buildToolState.recent = [];
    buildToolState.loading = false;
    buildToolState.errorMessage = null;
    return;
  }

  if (buildToolState.workspaceRoot && buildToolState.workspaceRoot !== root && buildToolState.activeTaskId) {
    await stopBuildTask();
  }
  buildToolState.workspaceRoot = root;
  buildToolState.loading = true;
  buildToolState.errorMessage = null;

  try {
    await loadLaunchConfigs(root);
    const [tasks, commands, packageJSON, makefile, lowerMakefile, taskfileYML, taskfileYAML] = await Promise.all([
      taskService.loadTasks(root),
      toolchainService.listToolchainCommands(),
      readOptional(workspacePath(root, "package.json")),
      readOptional(workspacePath(root, "Makefile")),
      readOptional(workspacePath(root, "makefile")),
      readOptional(workspacePath(root, "Taskfile.yml")),
      readOptional(workspacePath(root, "Taskfile.yaml")),
    ]);
    if (generation !== discoveryGeneration || buildToolState.workspaceRoot !== root) return;

    const discovered: BuildTaskItem[] = [
      ...standardTaskItems(tasks),
      ...launchItems(debugState.launchConfigs),
      ...commands.map(toolchainItem).filter((item): item is BuildTaskItem => item !== null),
      ...parsePackageScripts(packageJSON),
      ...parseMakeTargets(makefile || lowerMakefile).map((target) =>
        makeTerminalItem("make", target, `make: ${target}`, "make", [target])),
      ...parseTaskfileTargets(taskfileYML || taskfileYAML).map((target) =>
        makeTerminalItem("taskfile", target, `task: ${target}`, "task", [target])),
    ];
    buildToolState.tasks = dedupeBuildTasks(discovered);
    buildToolState.favorites = loadStoredList(FAVORITES_KEY, root)
      .filter((id) => buildToolState.tasks.some((task) => task.id === id));
    buildToolState.recent = loadStoredList(RECENT_KEY, root)
      .filter((id) => buildToolState.tasks.some((task) => task.id === id));
    buildToolState.selectedTaskId = buildToolState.tasks[0]?.id ?? null;
  } catch (error) {
    if (generation !== discoveryGeneration) return;
    buildToolState.errorMessage = error instanceof Error ? error.message : String(error);
    notifyError(`Failed to discover build tasks: ${buildToolState.errorMessage}`);
  } finally {
    if (generation === discoveryGeneration) buildToolState.loading = false;
  }
}

function addRecent(taskId: string): void {
  buildToolState.recent = [taskId, ...buildToolState.recent.filter((id) => id !== taskId)]
    .slice(0, RECENT_LIMIT);
  saveStoredList(RECENT_KEY, buildToolState.recent);
}

export function toggleBuildFavorite(taskId: string): void {
  if (!buildToolState.tasks.some((task) => task.id === taskId)) return;
  buildToolState.favorites = buildToolState.favorites.includes(taskId)
    ? buildToolState.favorites.filter((id) => id !== taskId)
    : [...buildToolState.favorites, taskId];
  saveStoredList(FAVORITES_KEY, buildToolState.favorites);
}

function reportProblems(task: BuildTaskItem, output: string): void {
  const problems = parseToolOutputToProblems(output, task.label);
  if (problems.length === 0) return;
  clearProblems();
  for (const problem of problems) {
    pushProblem(problem.severity, problem.file, problem.line, problem.column, problem.message, problem.source);
  }
}

export async function runBuildTask(taskId: string): Promise<void> {
  const item = buildToolState.tasks.find((task) => task.id === taskId);
  if (!item) return;
  const activeRun = buildToolState.activeTaskId
    ? buildToolState.runs[buildToolState.activeTaskId]
    : null;
  if (activeRun?.status === "running") {
    notifyWarning("A build task is already running");
    return;
  }

  const generation = ++runGeneration;
  const run: BuildRun = reactive({
    taskId,
    status: "running",
    output: "",
    durationMs: 0,
    startedAt: Date.now(),
    sessionId: null,
  });
  buildToolState.runs[taskId] = run;
  buildToolState.activeTaskId = taskId;
  buildToolState.selectedTaskId = taskId;
  addRecent(taskId);

  if (item.launch) {
    try {
      await launchWithConfig(item.launch);
      if (generation !== runGeneration || run.status === "cancelled") return;
      run.status = debugState.running ? "running" : "success";
      run.output = debugState.running ? `Running ${item.label}` : `Completed ${item.label}`;
    } catch (error) {
      if (generation !== runGeneration) return;
      run.status = "failed";
      run.output = error instanceof Error ? error.message : String(error);
    } finally {
      run.durationMs = Date.now() - run.startedAt;
      if (run.status !== "running" && buildToolState.activeTaskId === taskId) {
        buildToolState.activeTaskId = null;
      }
    }
    return;
  }

  if (item.source === "toolchain" && item.language === "go") {
    try {
      const commandId = item.id.slice("toolchain:".length);
      const result = await toolchainService.runToolchainCommand(commandId, "");
      if (generation !== runGeneration || run.status === "cancelled") return;
      run.output = result.output;
      run.durationMs = result.durationMs;
      run.status = result.success ? "success" : "failed";
      if (result.errors.length > 0) {
        clearProblems();
        for (const problem of result.errors) {
          pushProblem(
            problem.severity,
            problem.file,
            problem.line,
            problem.column,
            problem.message,
            problem.source,
          );
        }
      } else {
        reportProblems(item, result.output);
      }
      pushOutput(item.label, result.success ? "success" : "error", result.output || item.label);
    } catch (error) {
      if (generation !== runGeneration) return;
      run.status = "failed";
      run.output = error instanceof Error ? error.message : String(error);
      notifyError(`Failed to run ${item.label}: ${run.output}`);
    } finally {
      if (generation === runGeneration && run.status !== "cancelled") {
        run.durationMs ||= Date.now() - run.startedAt;
        if (buildToolState.activeTaskId === taskId) buildToolState.activeTaskId = null;
      }
    }
    return;
  }

  if (!item.task) return;
  const task: TaskDef = {
    ...item.task,
    args: [...(item.task.args ?? [])],
    env: { ...(item.task.env ?? {}) },
  };
  const cwd = resolveCwd(task, buildToolState.workspaceRoot);
  const sessionId = await createSession(cwd);
  if (!sessionId) {
    run.status = "failed";
    run.output = "Failed to create terminal session";
    buildToolState.activeTaskId = null;
    return;
  }
  run.sessionId = sessionId;
  const offOutput = onTerminalOutput(sessionId, (chunk) => {
    if (generation === runGeneration && run.status === "running") run.output += chunk;
  });

  try {
    const result = await runCommandInSessionCapturing(sessionId, composeCommandLine(task));
    if (generation !== runGeneration || run.status === "cancelled") return;
    run.output = result.output;
    run.status = result.exitCode === 0 ? "success" : "failed";
    reportProblems(item, result.output);
    pushOutput(item.label, result.exitCode === 0 ? "success" : "error", result.output || item.label);
  } catch (error) {
    if (generation !== runGeneration) return;
    run.status = "failed";
    run.output = error instanceof Error ? error.message : String(error);
    notifyError(`Failed to run ${item.label}: ${run.output}`);
  } finally {
    offOutput();
    run.durationMs = Date.now() - run.startedAt;
    if (buildToolState.activeTaskId === taskId && run.status !== "running") {
      buildToolState.activeTaskId = null;
    }
  }
}

export async function stopBuildTask(): Promise<void> {
  const taskId = buildToolState.activeTaskId;
  if (!taskId) return;
  const run = buildToolState.runs[taskId];
  if (!run || run.status !== "running") return;
  runGeneration++;
  run.status = "cancelled";
  run.durationMs = Date.now() - run.startedAt;
  const item = buildToolState.tasks.find((task) => task.id === taskId);
  if (item?.launch) {
    await stopDebugSession();
  } else if (run.sessionId) {
    await killSession(run.sessionId);
  }
  buildToolState.activeTaskId = null;
}

export async function rerunBuildTask(taskId?: string): Promise<void> {
  const id = taskId ?? buildToolState.recent[0] ?? buildToolState.selectedTaskId;
  if (id) await runBuildTask(id);
}

export function selectBuildTask(taskId: string): void {
  if (buildToolState.tasks.some((task) => task.id === taskId)) {
    buildToolState.selectedTaskId = taskId;
  }
}
