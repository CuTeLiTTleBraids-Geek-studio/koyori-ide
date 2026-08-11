import { beforeEach, describe, expect, it, vi } from "vitest";
import type { TaskDef } from "@/types";

const mocks = vi.hoisted(() => ({
  readFile: vi.fn(),
  loadTasks: vi.fn(),
  listToolchainCommands: vi.fn(),
  runToolchainCommand: vi.fn(),
  createSession: vi.fn(),
  runCommandInSessionCapturing: vi.fn(),
  killSession: vi.fn(),
  onTerminalOutput: vi.fn(),
  loadLaunchConfigs: vi.fn(),
  launchWithConfig: vi.fn(),
  stopDebugSession: vi.fn(),
  pushOutput: vi.fn(),
  pushProblem: vi.fn(),
  clearProblems: vi.fn(),
}));

vi.mock("@wailsio/runtime", () => ({ Events: { On: vi.fn() } }));
vi.mock("@/api/services", () => ({
  fileService: { readFile: mocks.readFile },
  taskService: { loadTasks: mocks.loadTasks },
  toolchainService: {
    listToolchainCommands: mocks.listToolchainCommands,
    runToolchainCommand: mocks.runToolchainCommand,
  },
}));
vi.mock("@/stores/terminal", () => ({
  createSession: mocks.createSession,
  writeToSession: vi.fn(),
  runCommandInSessionCapturing: mocks.runCommandInSessionCapturing,
  killSession: mocks.killSession,
  onTerminalOutput: mocks.onTerminalOutput,
}));
vi.mock("@/stores/debug", () => ({
  debugState: { launchConfigs: [], running: false },
  loadLaunchConfigs: mocks.loadLaunchConfigs,
  launchWithConfig: mocks.launchWithConfig,
  stopDebugSession: mocks.stopDebugSession,
}));
vi.mock("@/stores/goTarget", () => ({
  goTargetState: { current: { goos: "linux", goarch: "arm64" } },
}));
vi.mock("@/stores/output", () => ({
  pushOutput: mocks.pushOutput,
  pushProblem: mocks.pushProblem,
  clearProblems: mocks.clearProblems,
}));
vi.mock("@/lib/notifications", () => ({
  notifyError: vi.fn(),
  notifyWarning: vi.fn(),
}));

import { debugState } from "@/stores/debug";
import {
  buildToolState,
  dedupeBuildTasks,
  parseMakeTargets,
  parsePackageScripts,
  parseTaskfileTargets,
  refreshBuildTasks,
  rerunBuildTask,
  runBuildTask,
  stopBuildTask,
  toggleBuildFavorite,
  type BuildTaskItem,
} from "./buildTool";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

function terminalTask(overrides: Partial<BuildTaskItem> = {}): BuildTaskItem {
  return {
    id: "toolchain:go-test-race",
    label: "Go: Test with Race Detector",
    source: "toolchain",
    description: "race",
    language: "go",
    task: { label: "Go: Test with Race Detector", command: "go", args: ["test", "-race", "./..."] },
    ...overrides,
  };
}

describe("build tool store", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    buildToolState.workspaceRoot = "";
    buildToolState.tasks = [];
    buildToolState.loading = false;
    buildToolState.errorMessage = null;
    buildToolState.favorites = [];
    buildToolState.recent = [];
    buildToolState.runs = {};
    buildToolState.activeTaskId = null;
    buildToolState.selectedTaskId = null;
    (debugState.launchConfigs as unknown[]) = [];
    debugState.running = false;
    mocks.loadTasks.mockResolvedValue([]);
    mocks.loadLaunchConfigs.mockResolvedValue(undefined);
    mocks.listToolchainCommands.mockResolvedValue([]);
    mocks.runToolchainCommand.mockResolvedValue({
      success: true,
      output: "ok",
      errors: [],
      durationMs: 5,
      notInstalled: false,
    });
    mocks.readFile.mockRejectedValue(new Error("not found"));
    mocks.createSession.mockResolvedValue("build-session");
    mocks.onTerminalOutput.mockReturnValue(() => undefined);
    mocks.runCommandInSessionCapturing.mockResolvedValue({ exitCode: 0, output: "ok" });
    mocks.killSession.mockResolvedValue(undefined);
  });

  it("parses safe package scripts and rejects injectable names", () => {
    const tasks = parsePackageScripts(JSON.stringify({
      scripts: { build: "vite build", "test:unit": "vitest", "bad; echo pwned": "echo nope" },
    }));
    expect(tasks.map((task) => task.id)).toEqual(["npm:build", "npm:test:unit"]);
    expect(tasks[0].task).toMatchObject({ command: "npm", args: ["run", "build"] });
  });

  it("parses Makefile targets without pattern, metadata, or injected targets", () => {
    expect(parseMakeTargets(`
.PHONY: build test
build test: deps
deps:
%.o: %.c
bad;touch-pwned:
	`)).toEqual(["build", "test", "deps"]);
  });

  it("parses only top-level Taskfile task keys", () => {
    expect(parseTaskfileTargets(`version: '3'
tasks:
  build:
    cmds: [go build ./...]
  test:unit:
    desc: unit tests
  bad;touch-pwned:
    cmds: [echo no]
silent: true
`)).toEqual(["build", "test:unit"]);
  });

  it("deduplicates equivalent commands while preferring standard tasks", () => {
    const standard = terminalTask({
      id: "task:build",
      label: "Standard build",
      source: "task",
      task: { label: "Standard build", command: "go", args: ["build", "./..."] },
    });
    const generated = terminalTask({
      id: "toolchain:go-build",
      label: "Go: Build",
      task: { label: "Go: Build", command: "go", args: ["build", "./..."] },
    });
    expect(dedupeBuildTasks([generated, standard])).toEqual([standard]);
  });

  it("aggregates standard tasks/launch, 10B Go commands, and discovered targets", async () => {
    mocks.loadTasks.mockResolvedValue([
      { label: "VS Code build", command: "go", args: ["build", "./..."] },
    ]);
    (debugState.launchConfigs as unknown[]) = [{ name: "Launch API", kind: "package", dir: "C:/repo" }];
    mocks.listToolchainCommands.mockResolvedValue([
      { id: "go-build", label: "Go: Build", language: "go", command: "go build", args: ["./..."] },
      { id: "go-test-race", label: "Go: Race", language: "go", command: "go", args: ["test", "-race", "./..."] },
      { id: "go-bench", label: "Go: Bench", language: "go", command: "go", args: ["test", "-bench=."] },
      { id: "go-generate", label: "Go: Generate", language: "go", command: "go", args: ["generate", "./..."] },
      { id: "go-work-sync", label: "Go: Work Sync", language: "go", command: "go", args: ["work", "sync"] },
    ]);
    mocks.readFile.mockImplementation(async (path: string) => {
      if (path.endsWith("package.json")) return '{"scripts":{"dev":"vite"}}';
      if (path.endsWith("Makefile")) return "release:\n\tgo build";
      if (path.endsWith("Taskfile.yml")) return "tasks:\n  verify:\n    cmds: [go test ./...]";
      throw new Error("not found");
    });

    await refreshBuildTasks("C:/repo");

    expect(buildToolState.tasks.map((task) => task.id)).toEqual(expect.arrayContaining([
      "task:VS Code build",
      "launch:Launch API",
      "toolchain:go-test-race",
      "toolchain:go-bench",
      "toolchain:go-generate",
      "toolchain:go-work-sync",
      "npm:dev",
      "make:release",
      "taskfile:verify",
    ]));
    expect(buildToolState.tasks.some((task) => task.id === "toolchain:go-build")).toBe(false);
  });

  it("discards stale discovery after a workspace switch", async () => {
    const first = deferred<TaskDef[]>();
    mocks.loadTasks
      .mockReturnValueOnce(first.promise)
      .mockResolvedValueOnce([{ label: "new", command: "go", args: ["test"] }]);

    const oldRefresh = refreshBuildTasks("C:/old");
    await refreshBuildTasks("C:/new");
    first.resolve([{ label: "old", command: "go", args: ["build"] }]);
    await oldRefresh;

    expect(buildToolState.workspaceRoot).toBe("C:/new");
    expect(buildToolState.tasks.map((task) => task.id)).toContain("task:new");
    expect(buildToolState.tasks.map((task) => task.id)).not.toContain("task:old");
  });

  it("runs Go catalog tasks through the structured Toolchain path", async () => {
    const task = terminalTask();
    buildToolState.workspaceRoot = "C:/repo";
    buildToolState.tasks = [task];

    await runBuildTask(task.id);

    expect(mocks.runToolchainCommand).toHaveBeenCalledWith("go-test-race", "");
    expect(mocks.createSession).not.toHaveBeenCalled();
    expect(buildToolState.runs[task.id]).toMatchObject({ status: "success", output: expect.stringContaining("ok") });
    expect(buildToolState.recent[0]).toBe(task.id);
  });

  it("stops a terminal task and ignores its late completion", async () => {
    const task = terminalTask({
      id: "npm:test",
      label: "npm: test",
      source: "npm",
      language: undefined,
      task: { label: "npm: test", command: "npm", args: ["run", "test"] },
    });
    const run = deferred<{ exitCode: number; output: string }>();
    mocks.runCommandInSessionCapturing.mockReturnValue(run.promise);
    buildToolState.workspaceRoot = "C:/repo";
    buildToolState.tasks = [task];

    const running = runBuildTask(task.id);
    await vi.waitFor(() => expect(mocks.runCommandInSessionCapturing).toHaveBeenCalled());
    await stopBuildTask();
    expect(mocks.killSession).toHaveBeenCalledWith("build-session");
    expect(buildToolState.runs[task.id].status).toBe("cancelled");

    run.resolve({ exitCode: 0, output: "late success" });
    await running;
    expect(buildToolState.runs[task.id].status).toBe("cancelled");
  });

  it("soft-cancels a Go Toolchain run and ignores its late result", async () => {
    const task = terminalTask();
    const result = deferred<{
      success: boolean;
      output: string;
      errors: never[];
      durationMs: number;
      notInstalled: boolean;
    }>();
    mocks.runToolchainCommand.mockReturnValue(result.promise);
    buildToolState.workspaceRoot = "C:/repo";
    buildToolState.tasks = [task];

    const running = runBuildTask(task.id);
    await vi.waitFor(() => expect(mocks.runToolchainCommand).toHaveBeenCalled());
    await stopBuildTask();
    expect(buildToolState.runs[task.id].status).toBe("cancelled");
    expect(mocks.killSession).not.toHaveBeenCalled();

    result.resolve({ success: true, output: "late", errors: [], durationMs: 10, notInstalled: false });
    await running;
    expect(buildToolState.runs[task.id].status).toBe("cancelled");
  });

  it("persists workspace-scoped favorites and recent tasks and reruns the last task", async () => {
    const task = terminalTask();
    buildToolState.workspaceRoot = "C:/repo";
    buildToolState.tasks = [task];
    toggleBuildFavorite(task.id);
    expect(buildToolState.favorites).toEqual([task.id]);
    expect(localStorage.getItem("koyori-ide.build.favorites:C:/repo")).toContain(task.id);

    await runBuildTask(task.id);
    await rerunBuildTask();
    expect(mocks.runToolchainCommand).toHaveBeenCalledTimes(2);
  });
});
