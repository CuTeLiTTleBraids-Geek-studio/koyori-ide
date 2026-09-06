import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

vi.mock("@wailsio/runtime", () => ({
  Events: { On: vi.fn() },
}));

vi.mock("@/api/services", () => ({
  taskService: {
    loadTasks: vi.fn(),
    requestExecutionApproval: vi.fn().mockResolvedValue("approval-1"),
    executeApproved: vi.fn().mockResolvedValue({
      command: "", cwd: "", stdout: "", stderr: "", exitCode: 0,
      durationMs: 1, riskLevel: "safe", blocked: false,
    }),
    stop: vi.fn().mockResolvedValue(undefined),
  },
}));

vi.mock("@/stores/app", () => ({
  appState: { terminalVisible: false, currentProject: null },
}));

vi.mock("@/stores/output", () => ({
  pushOutput: vi.fn(),
}));

vi.mock("@/lib/notifications", () => ({
  notifyError: vi.fn(),
  notifySuccess: vi.fn(),
  notifyWarning: vi.fn(),
}));

import {
  taskState,
  loadTasks,
  composeCommandLine,
  resolveCwd,
  runTask,
  hasTasks,
  cleanupTaskStoreTimers,
} from "./tasks";
import { taskService } from "@/api/services";
import { pushOutput } from "@/stores/output";
import type { TaskDef } from "@/types";

describe("tasks store", () => {
  beforeEach(() => {
    cleanupTaskStoreTimers();
    vi.useRealTimers();
    vi.clearAllMocks();
    taskState.tasks = [];
    taskState.loading = false;
    taskState.errorMessage = null;
  });

  afterEach(() => {
    cleanupTaskStoreTimers();
    vi.useRealTimers();
  });

  describe("loadTasks", () => {
    it("clears tasks when root is empty", async () => {
      taskState.tasks = [{ label: "x", command: "y" }];
      await loadTasks("");
      expect(taskState.tasks).toEqual([]);
      expect(taskState.errorMessage).toBeNull();
      expect(taskService.loadTasks).not.toHaveBeenCalled();
    });

    it("loads tasks from backend", async () => {
      vi.mocked(taskService.loadTasks).mockResolvedValue([
        { label: "build", command: "go", args: ["build", "./..."] },
      ]);
      await loadTasks("/proj");
      expect(taskState.tasks.length).toBe(1);
      expect(taskState.tasks[0].label).toBe("build");
      expect(taskState.loading).toBe(false);
      expect(taskState.errorMessage).toBeNull();
    });

    it("surfaces backend errors", async () => {
      taskState.tasks = [{ label: "last-good", command: "echo" }];
      vi.mocked(taskService.loadTasks).mockRejectedValue(new Error("parse failed"));
      await loadTasks("/proj");
      expect(taskState.tasks).toEqual([{ label: "last-good", command: "echo" }]);
      expect(taskState.errorMessage).toBe("parse failed");
      expect(taskState.loading).toBe(false);
    });

    it("does not let an older workspace response overwrite a newer load", async () => {
      let resolveFirst!: (value: TaskDef[]) => void;
      let resolveSecond!: (value: TaskDef[]) => void;
      vi.mocked(taskService.loadTasks)
        .mockImplementationOnce(() => new Promise((resolve) => { resolveFirst = resolve; }))
        .mockImplementationOnce(() => new Promise((resolve) => { resolveSecond = resolve; }));

      const first = loadTasks("/first");
      const second = loadTasks("/second");
      resolveSecond([{ label: "second", command: "echo" }]);
      await second;
      resolveFirst([{ label: "first", command: "echo" }]);
      await first;

      expect(taskState.tasks).toEqual([{ label: "second", command: "echo" }]);
      expect(taskState.loading).toBe(false);
    });
  });

  describe("hasTasks computed", () => {
    it("is false when no tasks", () => {
      expect(hasTasks.value).toBe(false);
    });
    it("is true when tasks exist", () => {
      taskState.tasks = [{ label: "x", command: "y" }];
      expect(hasTasks.value).toBe(true);
    });
  });

  describe("composeCommandLine", () => {
    it("returns command alone when no args", () => {
      expect(composeCommandLine({ label: "x", command: "ls" })).toBe("ls");
    });
    it("quotes args", () => {
      expect(composeCommandLine({ label: "x", command: "go", args: ["build", "./..."] }))
        .toBe("go 'build' './...'");
    });
    it("escapes embedded single quotes", () => {
      expect(composeCommandLine({ label: "x", command: "echo", args: ["it's"] }))
        .toBe("echo 'it'\\''s'");
    });

  });

  describe("resolveCwd", () => {
    it("returns project root when no cwd", () => {
      expect(resolveCwd({ label: "x", command: "y" }, "/proj")).toBe("/proj");
    });
    it("joins relative cwd to root", () => {
      expect(resolveCwd({ label: "x", command: "y", cwd: "src" }, "/proj")).toBe("/proj/src");
    });
    it("strips trailing slash from root before joining", () => {
      expect(resolveCwd({ label: "x", command: "y", cwd: "src" }, "/proj/")).toBe("/proj/src");
    });
    it("uses absolute cwd as-is", () => {
      expect(resolveCwd({ label: "x", command: "y", cwd: "/abs/path" }, "/proj")).toBe("/abs/path");
    });
    it("uses absolute windows cwd as-is", () => {
      expect(resolveCwd({ label: "x", command: "y", cwd: "C:\\abs" }, "/proj")).toBe("C:\\abs");
    });
  });

  describe("runTask", () => {
    it("requests and redeems a backend Agent capability", async () => {
      await runTask({ label: "build", command: "go", args: ["build"] }, "/proj");
      const [executionId] = vi.mocked(taskService.requestExecutionApproval).mock.calls[0];
      expect(taskService.requestExecutionApproval).toHaveBeenCalledWith(
        executionId, "go 'build'", "/proj",
      );
      expect(taskService.executeApproved).toHaveBeenCalledWith(
        executionId, "go 'build'", "/proj", "approval-1",
      );
      expect(pushOutput).toHaveBeenCalledWith("task", "info", expect.stringContaining("build"));
    });

    it("uses task cwd when provided", async () => {
      await runTask({ label: "x", command: "ls", cwd: "sub" }, "/proj");
      expect(taskService.requestExecutionApproval).toHaveBeenCalledWith(
        expect.any(String), "ls", "/proj/sub",
      );
    });

    it("does not execute when backend approval fails", async () => {
      vi.mocked(taskService.requestExecutionApproval).mockRejectedValueOnce(new Error("denied"));
      await runTask({ label: "x", command: "ls" }, "/proj");
      expect(taskService.executeApproved).not.toHaveBeenCalled();
    });

    it("stops an in-flight tracked execution during runtime cleanup", async () => {
      let resolveExecution: ((result: Awaited<ReturnType<typeof taskService.executeApproved>>) => void) | undefined;
      vi.mocked(taskService.executeApproved).mockImplementationOnce(
        () => new Promise((resolve) => {
          resolveExecution = resolve;
        }),
      );
      const runPromise = runTask({ label: "build", command: "go" }, "/proj");
      await Promise.resolve();
      await Promise.resolve();
      const [executionId] = vi.mocked(taskService.executeApproved).mock.calls[0];

      cleanupTaskStoreTimers();
      expect(taskService.stop).toHaveBeenCalledWith(executionId);
      resolveExecution?.({
        command: "go", cwd: "/proj", stdout: "", stderr: "[command terminated]",
        exitCode: -1, durationMs: 1, riskLevel: "safe", blocked: false,
      });
      await runPromise;
    });

    it("stops and does not redeem after cleanup while approval is pending", async () => {
      let resolveApproval: ((token: string) => void) | undefined;
      vi.mocked(taskService.requestExecutionApproval).mockImplementationOnce(
        () => new Promise<string>((resolve) => {
          resolveApproval = resolve;
        }),
      );

      const runPromise = runTask({ label: "build", command: "go" }, "/proj");
      await Promise.resolve();
      const [executionId] = vi.mocked(taskService.requestExecutionApproval).mock.calls[0];
      cleanupTaskStoreTimers();
      resolveApproval?.("stale-approval");
      await runPromise;

      expect(taskService.stop).toHaveBeenCalledWith(executionId);
      expect(taskService.executeApproved).not.toHaveBeenCalled();
    });
  });
});
