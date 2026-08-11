/**
 * toolchain store 的单元测试（G-FEAT-03）。
 *
 * 该 store 负责编排后端 ToolchainService：
 *   - 列出工作区可用的工具链命令
 *   - 探测本机已安装的工具链二进制
 *   - 运行某条命令并把输出/诊断分别路由到 Output / Problems 面板
 *
 * 测试通过 vi.hoisted + vi.mock 模式桩接 @/api/services 以及 store 依赖的
 * app / output / notifications / i18n 模块，仅校验编排逻辑本身。
 */
import { describe, it, expect, beforeEach, vi } from "vitest";

// 使用 vi.hoisted 提升 mock 句柄，使 vi.mock 工厂与测试体共享同一份桩接对象。
const mocks = vi.hoisted(() => {
  // toolchainService：被测 store 唯一的后端依赖。
  const toolchainService = {
    listToolchainCommands: vi.fn(),
    runToolchainCommand: vi.fn(),
    cancelTestAtCursor: vi.fn(),
    detectToolchains: vi.fn(),
  };
  // output store：被测 store 会向其推送输出与诊断，桩接以便断言调用参数。
  const pushOutput = vi.fn();
  const pushProblem = vi.fn();
  const clearProblems = vi.fn();
  // 通知桩接。
  const notifyWarning = vi.fn();
  const notifyError = vi.fn();
  // i18n：默认直接返回 key，方便断言传入了哪个翻译键。
  const translate = vi.fn((key: string) => key);
  // appState：被测 store 会写入 terminalVisible 与 bottomPanelView，
  // 使用可变对象以便在测试中观察这些副作用。
  const appState = {
    terminalVisible: false,
    bottomPanelView: "" as string,
  };
  return {
    toolchainService,
    pushOutput,
    pushProblem,
    clearProblems,
    notifyWarning,
    notifyError,
    translate,
    appState,
  };
});

vi.mock("@/api/services", () => ({
  toolchainService: mocks.toolchainService,
}));

vi.mock("@/stores/app", () => ({ appState: mocks.appState }));

vi.mock("@/stores/output", () => ({
  pushOutput: mocks.pushOutput,
  pushProblem: mocks.pushProblem,
  clearProblems: mocks.clearProblems,
}));

vi.mock("@/lib/notifications", () => ({
  notifyWarning: mocks.notifyWarning,
  notifyError: mocks.notifyError,
}));

vi.mock("@/lib/i18n", () => ({ translate: mocks.translate }));

import {
  toolchainState,
  hasToolchainCommands,
  loadToolchainCommands,
  detectToolchains,
  runToolchainCommand,
  cancelTestAtCursor,
} from "./toolchain";
import type { ToolchainCommand, ToolchainResult } from "@/types";

// ---- 真实合理的 mock 数据 ----

const goBuildCmd: ToolchainCommand = {
  id: "go-build",
  label: "Go: Build",
  language: "go",
  command: "go",
  args: ["build", "./..."],
  description: "Build all Go packages in the workspace",
};

const tsLintCmd: ToolchainCommand = {
  id: "ts-lint",
  label: "TypeScript: Lint",
  language: "typescript",
  command: "eslint",
  args: ["src", "--ext", ".ts"],
};

const successWithOutput: ToolchainResult = {
  success: true,
  output: "✓ Build succeeded (2.3s)",
  errors: [],
  durationMs: 2300,
  notInstalled: false,
};

const successNoOutput: ToolchainResult = {
  success: true,
  output: "",
  errors: [],
  durationMs: 50,
  notInstalled: false,
};

const notInstalledResult: ToolchainResult = {
  success: false,
  output: "",
  errors: [],
  durationMs: 0,
  notInstalled: true,
  installCmd: "go install golang.org/x/tools/gopls@latest",
};

const failedWithDiagnostics: ToolchainResult = {
  success: false,
  output: "main.go:10:5: undefined: foo",
  errors: [
    {
      file: "main.go",
      line: 10,
      column: 5,
      message: "undefined: foo",
      severity: "error",
      source: "go-build",
    },
    {
      file: "util.go",
      line: 3,
      column: 1,
      message: "unused variable bar",
      severity: "warning",
      source: "go-build",
    },
  ],
  durationMs: 800,
  notInstalled: false,
};

const failedNoDiagnostics: ToolchainResult = {
  success: false,
  output: "exit status 1",
  errors: [],
  durationMs: 300,
  notInstalled: false,
};

const detectedMap: Record<string, boolean> = {
  go: true,
  node: true,
  rustc: false,
  python: true,
};

describe("toolchain store", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    // 重置被测 store 的状态，避免用例间相互污染。
    toolchainState.commands = [];
    toolchainState.running = false;
    toolchainState.runningId = null;
    toolchainState.detected = {};
    // 重置 appState 副作用字段。
    mocks.appState.terminalVisible = false;
    mocks.appState.bottomPanelView = "";
    // 为 service 方法设置安全默认返回，避免未配置用例意外触发拒绝路径。
    mocks.toolchainService.listToolchainCommands.mockResolvedValue([]);
    mocks.toolchainService.runToolchainCommand.mockResolvedValue(successWithOutput);
    mocks.toolchainService.cancelTestAtCursor.mockResolvedValue(false);
    mocks.toolchainService.detectToolchains.mockResolvedValue({});
  });

  describe("初始状态", () => {
    it("commands 为空、running 为 false、runningId 为 null、detected 为空对象", () => {
      expect(toolchainState.commands).toEqual([]);
      expect(toolchainState.running).toBe(false);
      expect(toolchainState.runningId).toBeNull();
      expect(toolchainState.detected).toEqual({});
    });
  });

  describe("hasToolchainCommands computed", () => {
    it("无命令时返回 false", () => {
      expect(hasToolchainCommands.value).toBe(false);
    });

    it("有命令时返回 true", () => {
      toolchainState.commands = [goBuildCmd];
      expect(hasToolchainCommands.value).toBe(true);
    });
  });

  describe("loadToolchainCommands", () => {
    it("成功时调用 listToolchainCommands 并填充 commands", async () => {
      mocks.toolchainService.listToolchainCommands.mockResolvedValue([
        goBuildCmd,
        tsLintCmd,
      ]);
      await loadToolchainCommands();
      expect(mocks.toolchainService.listToolchainCommands).toHaveBeenCalledTimes(1);
      expect(toolchainState.commands).toHaveLength(2);
      expect(toolchainState.commands[0].id).toBe("go-build");
      expect(toolchainState.commands[1].label).toBe("TypeScript: Lint");
    });

    it("后端报错时静默吞掉异常并保留原 commands（best-effort）", async () => {
      toolchainState.commands = [goBuildCmd];
      mocks.toolchainService.listToolchainCommands.mockRejectedValue(
        new Error("backend offline"),
      );
      // 不应抛出。
      await expect(loadToolchainCommands()).resolves.toBeUndefined();
      // 原 commands 保持不变。
      expect(toolchainState.commands).toEqual([goBuildCmd]);
    });
  });

  describe("detectToolchains", () => {
    it("成功时更新 detected 并返回同一份 map", async () => {
      mocks.toolchainService.detectToolchains.mockResolvedValue(detectedMap);
      const result = await detectToolchains();
      expect(mocks.toolchainService.detectToolchains).toHaveBeenCalledTimes(1);
      expect(toolchainState.detected).toEqual(detectedMap);
      expect(result).toEqual(detectedMap);
      expect(result.go).toBe(true);
      expect(result.rustc).toBe(false);
    });

    it("后端报错时重置 detected 为空对象并返回空 map", async () => {
      // 先人为塞入旧数据，验证失败路径会清空。
      toolchainState.detected = { go: true };
      mocks.toolchainService.detectToolchains.mockRejectedValue(
        new Error("detect failed"),
      );
      const result = await detectToolchains();
      expect(toolchainState.detected).toEqual({});
      expect(result).toEqual({});
    });
  });

  describe("runToolchainCommand", () => {
    it("成功执行(带输出)：调用 service、推送 info 输出、返回 result 并展示终端", async () => {
      toolchainState.commands = [goBuildCmd];
      mocks.toolchainService.runToolchainCommand.mockResolvedValue(successWithOutput);

      const result = await runToolchainCommand("go-build");

      // 缺省 filePath 时传空串。
      expect(mocks.toolchainService.runToolchainCommand).toHaveBeenCalledWith(
        "go-build",
        "",
      );
      // 返回原始 result。
      expect(result).toBe(successWithOutput);
      // 成功 + 有输出 → info 级别输出，source 使用命令 label。
      expect(mocks.pushOutput).toHaveBeenCalledWith(
        "Go: Build",
        "info",
        successWithOutput.output,
      );
      // 终端面板可见。
      expect(mocks.appState.terminalVisible).toBe(true);
      // 成功路径聚焦 output 视图。
      expect(mocks.appState.bottomPanelView).toBe("output");
    });

    it("成功执行(无输出)：推送 success 级别的 completedNoOutput 文案", async () => {
      toolchainState.commands = [goBuildCmd];
      mocks.toolchainService.runToolchainCommand.mockResolvedValue(successNoOutput);

      await runToolchainCommand("go-build");

      // 无输出且成功 → 走 completedNoOutput 分支，severity 为 success。
      expect(mocks.pushOutput).toHaveBeenCalledWith(
        "Go: Build",
        "success",
        "toolchain.completedNoOutput",
      );
    });

    it("传入 filePath 时原样转发给 service", async () => {
      mocks.toolchainService.runToolchainCommand.mockResolvedValue(successWithOutput);

      await runToolchainCommand("go-build", "/proj/src/main.go");

      expect(mocks.toolchainService.runToolchainCommand).toHaveBeenCalledWith(
        "go-build",
        "/proj/src/main.go",
      );
    });

    it("状态管理：执行中 running=true/runningId=cmdId，结束后恢复", async () => {
      // 在 service 解析前捕获 store 状态，验证运行态被正确置位。
      let capturedRunning = false;
      let capturedRunningId: string | null = null;
      mocks.toolchainService.runToolchainCommand.mockImplementation(async () => {
        capturedRunning = toolchainState.running;
        capturedRunningId = toolchainState.runningId;
        return successWithOutput;
      });

      await runToolchainCommand("go-build");

      // 执行期间的状态。
      expect(capturedRunning).toBe(true);
      expect(capturedRunningId).toBe("go-build");
      // finally 块恢复初始态。
      expect(toolchainState.running).toBe(false);
      expect(toolchainState.runningId).toBeNull();
    });

    it("并发拒绝：已有命令运行时直接返回 null 并发出告警", async () => {
      toolchainState.running = true;
      // 即使误触发 service，也不应真正调用。
      const result = await runToolchainCommand("go-build");
      expect(result).toBeNull();
      expect(mocks.toolchainService.runToolchainCommand).not.toHaveBeenCalled();
      expect(mocks.notifyWarning).toHaveBeenCalledWith("toolchain.alreadyRunning");
    });

    it("工具未安装(notInstalled)：发出告警并聚焦 output 视图，不当作硬错误", async () => {
      toolchainState.commands = [goBuildCmd];
      mocks.toolchainService.runToolchainCommand.mockResolvedValue(notInstalledResult);

      const result = await runToolchainCommand("go-build");

      // 仍返回 result（未安装不是异常路径）。
      expect(result).toBe(notInstalledResult);
      // 告警携带安装命令。
      expect(mocks.notifyWarning).toHaveBeenCalledWith(
        "toolchain.notInstalled",
      );
      expect(mocks.translate).toHaveBeenCalledWith("toolchain.notInstalled", expect.anything());
      // 未安装路径聚焦 output。
      expect(mocks.appState.bottomPanelView).toBe("output");
    });

    it("失败但带诊断：清空旧问题并逐条推送，聚焦 problems 视图", async () => {
      toolchainState.commands = [goBuildCmd];
      mocks.toolchainService.runToolchainCommand.mockResolvedValue(failedWithDiagnostics);

      await runToolchainCommand("go-build");

      // 失败 + 有 output → error 级别输出。
      expect(mocks.pushOutput).toHaveBeenCalledWith(
        "Go: Build",
        "error",
        failedWithDiagnostics.output,
      );
      // 先清空再逐条推送。
      expect(mocks.clearProblems).toHaveBeenCalledTimes(1);
      expect(mocks.pushProblem).toHaveBeenCalledTimes(failedWithDiagnostics.errors.length);
      // 第一条 error 诊断：severity/file/line/column/message/source 全量透传。
      expect(mocks.pushProblem).toHaveBeenNthCalledWith(
        1,
        "error",
        "main.go",
        10,
        5,
        "undefined: foo",
        "go-build",
      );
      // 第二条 warning 诊断。
      expect(mocks.pushProblem).toHaveBeenNthCalledWith(
        2,
        "warning",
        "util.go",
        3,
        1,
        "unused variable bar",
        "go-build",
      );
      // 有诊断时聚焦 problems 视图。
      expect(mocks.appState.bottomPanelView).toBe("problems");
    });

    it("失败但无诊断：聚焦 output 视图且不推送任何 problem", async () => {
      toolchainState.commands = [goBuildCmd];
      mocks.toolchainService.runToolchainCommand.mockResolvedValue(failedNoDiagnostics);

      await runToolchainCommand("go-build");

      expect(mocks.clearProblems).not.toHaveBeenCalled();
      expect(mocks.pushProblem).not.toHaveBeenCalled();
      expect(mocks.appState.bottomPanelView).toBe("output");
    });

    it("service 抛错：发出错误通知、推送 error 输出、返回 null 且 running 恢复", async () => {
      toolchainState.commands = [goBuildCmd];
      mocks.toolchainService.runToolchainCommand.mockRejectedValue(
        new Error("spawn: go ENOENT"),
      );

      const result = await runToolchainCommand("go-build");

      expect(result).toBeNull();
      // 错误通知携带后端错误信息。
      expect(mocks.notifyError).toHaveBeenCalledWith("toolchain.runFailed");
      expect(mocks.translate).toHaveBeenCalledWith(
        "toolchain.runFailed",
        expect.objectContaining({ error: "spawn: go ENOENT" }),
      );
      // 错误输出推送。
      expect(mocks.pushOutput).toHaveBeenCalledWith(
        "Go: Build",
        "error",
        "spawn: go ENOENT",
      );
      // 异常路径聚焦 output 视图。
      expect(mocks.appState.bottomPanelView).toBe("output");
      // finally 仍恢复运行态。
      expect(toolchainState.running).toBe(false);
      expect(toolchainState.runningId).toBeNull();
    });

    it("sourceLabel：未在 commands 中命中时回退为 cmdId", async () => {
      // commands 为空，source 应直接使用 cmdId。
      mocks.toolchainService.runToolchainCommand.mockResolvedValue(successWithOutput);

      await runToolchainCommand("orphan-cmd");

      expect(mocks.pushOutput).toHaveBeenCalledWith(
        "orphan-cmd",
        "info",
        successWithOutput.output,
      );
    });
  });

  describe("cancelTestAtCursor", () => {
    it("does not cancel a non-test toolchain command", async () => {
      toolchainState.running = true;
      toolchainState.runningId = "go-build";

      await expect(cancelTestAtCursor()).resolves.toBe(false);
      expect(mocks.toolchainService.cancelTestAtCursor).not.toHaveBeenCalled();
    });

    it("cancels the active test-at-cursor run", async () => {
      toolchainState.running = true;
      toolchainState.runningId = "test-cursor";
      mocks.toolchainService.cancelTestAtCursor.mockResolvedValue(true);

      await expect(cancelTestAtCursor()).resolves.toBe(true);
      expect(mocks.toolchainService.cancelTestAtCursor).toHaveBeenCalledOnce();
    });
  });
});
