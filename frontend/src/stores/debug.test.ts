/**
 * prompt-5: 调试器增强 — 前端单元测试
 *
 * 覆盖以下新增功能：
 * - 函数断点 (FunctionBreakpoints) 添加/删除/应用
 * - SetVariable 包装
 * - RestartFrame 包装
 * - GetInlineValues 包装
 * - 多会话: StartSession / StopSession / SetActiveSession / ListSessions
 */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

vi.mock("@wailsio/runtime", () => ({
  Events: { On: vi.fn(), Emit: vi.fn() },
}));

vi.mock("../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/debugservice.js", () => ({
  LaunchWithConfig: vi.fn(),
  SelectBrowserTarget: vi.fn(),
  GetVariables: vi.fn(),
}));

// prompt-5: 模拟 debugService — 所有方法都是 vi.fn()，可单独 mockResolvedValue
vi.mock("@/api/services", () => ({
  fileService: {
    readFile: vi.fn(),
  },
  debugService: {
    isAvailable: vi.fn().mockResolvedValue(true),
    getState: vi.fn().mockResolvedValue({
      session: { running: false, address: "", mode: "", message: "ready" },
      breakpoints: [],
      stack: [],
      locals: [],
      watches: [],
      stopReason: "",
    }),
    launchPackage: vi.fn(),
    launchTest: vi.fn(),
    launchNode: vi.fn(),
    launchWithConfig: vi.fn(),
    restart: vi.fn(),
    stop: vi.fn(),
    setBreakpoint: vi.fn(),
    setBreakpointEx: vi.fn(),
    setBreakpointCondition: vi.fn(),
    removeBreakpoint: vi.fn(),
    toggleBreakpoint: vi.fn(),
    listBreakpoints: vi.fn().mockResolvedValue([]),
    continue: vi.fn(),
    stepOver: vi.fn(),
    stepIn: vi.fn(),
    stepOut: vi.fn(),
    pause: vi.fn(),
    refreshStackAndLocals: vi.fn().mockResolvedValue(undefined),
    selectFrame: vi.fn(),
    evaluate: vi.fn(),
    addWatch: vi.fn(),
    removeWatch: vi.fn(),
    refreshWatches: vi.fn(),
    listWatches: vi.fn().mockResolvedValue([]),
    attachDelve: vi.fn(),
    probeDelveTCP: vi.fn(),
    clearLastError: vi.fn(),
    // prompt-5: 新增方法
    setFunctionBreakpoints: vi.fn().mockResolvedValue(undefined),
    listFunctionBreakpoints: vi.fn().mockResolvedValue([]),
    setVariable: vi.fn(),
    restartFrame: vi.fn().mockResolvedValue(undefined),
    getInlineValues: vi.fn().mockResolvedValue([]),
    startSession: vi.fn(),
    stopSession: vi.fn().mockResolvedValue(undefined),
    getActiveSession: vi.fn().mockResolvedValue("default"),
    setActiveSession: vi.fn().mockResolvedValue(undefined),
    listSessions: vi.fn().mockResolvedValue([]),
  },
}));

vi.mock("@/stores/app", () => ({
  appState: { currentProject: "/proj", terminalVisible: false },
}));

vi.mock("@/stores/output", () => ({
  pushOutput: vi.fn(),
}));

vi.mock("@/lib/notifications", () => ({
  notifyError: vi.fn(),
  notifySuccess: vi.fn(),
  notifyInfo: vi.fn(),
  notifyWarning: vi.fn(),
}));

import {
  debugState,
  addFunctionBreakpoint,
  removeFunctionBreakpoint,
  applyFunctionBreakpoints,
  setVariable,
  restartFrame,
  refreshInlineValues,
  startEditVariable,
  cancelEditVariable,
  switchSession,
  startDebugSession,
  stopDebugSessionByID,
  refreshSessions,
  loadLaunchConfigs,
  launchWithConfig,
  launchCurrentFile,
  selectBrowserTarget,
  applyDebugSnapshot,
  initDebugRuntime,
  cleanupDebugRuntime,
  fetchVariables,
  toggleVariableExpansion,
  clearExpandedVariables,
} from "./debug";
import { debugService, fileService } from "@/api/services";
import * as DebugServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/debugservice.js";

describe("prompt-5: debug store enhancements", () => {
  beforeEach(() => {
    cleanupDebugRuntime();
    vi.useRealTimers();
    vi.clearAllMocks();
    // 重置 debugState
    debugState.functionBreakpoints = [];
    debugState.sessions = [];
    debugState.activeSessionID = "";
    debugState.inlineValues = [];
    debugState.editingVarName = "";
    debugState.editingVarValue = "";
    debugState.editingVarRef = 0;
    debugState.newFuncBpName = "";
    debugState.newFuncBpCondition = "";
    debugState.newFuncBpHitCondition = "";
    debugState.running = false;
    debugState.stopped = false;
    debugState.busy = false;
    debugState.browserTargets = [];
    debugState.browserTargetId = "";
    debugState.browserConsole = [];
    debugState.browserNetwork = [];
    localStorage.clear();
  });

  afterEach(() => {
    cleanupDebugRuntime();
    vi.useRealTimers();
  });

  describe("debug runtime lifecycle", () => {
    it("starts polling for a stopped session and clears it during cleanup", () => {
      vi.useFakeTimers();

      initDebugRuntime();
      expect(debugState.pollTimer).toBe(0);

      debugState.stopped = true;
      initDebugRuntime();
      expect(debugState.pollTimer).not.toBe(0);
      expect(vi.getTimerCount()).toBe(1);

      cleanupDebugRuntime();
      expect(debugState.pollTimer).toBe(0);
      expect(vi.getTimerCount()).toBe(0);
    });
  });

  describe("VS Code launch.json", () => {
    it("loads JSONC Go and Node launch configurations", async () => {
      (fileService.readFile as ReturnType<typeof vi.fn>).mockResolvedValue(`{
        // Keep comment-like text inside strings.
        "version": "0.2.0",
        "configurations": [
          {
            "name": "Go API",
            "type": "go",
            "request": "launch",
            "mode": "debug",
            "program": "${"${workspaceFolder}"}/cmd/api",
            "args": ["https://example.com/a//b",],
            "env": {"PORT": "8080",},
          },
          {
            "name": "Node app",
            "type": "pwa-node",
            "request": "launch",
            "program": "${"${workspaceFolder}"}/server.js",
            "cwd": "${"${workspaceFolder}"}",
          },
        ],
      }`);

      await loadLaunchConfigs("C:/repo");

      expect(debugState.launchConfigs).toEqual([
        expect.objectContaining({
          name: "Go API",
          kind: "package",
          dir: "C:/repo/cmd/api",
          args: ["https://example.com/a//b"],
          env: { PORT: "8080" },
        }),
        expect.objectContaining({
          name: "Node app",
          kind: "node",
          program: "C:/repo/server.js",
          dir: "C:/repo",
        }),
      ]);
      expect(fileService.readFile).toHaveBeenCalledWith(
        "C:/repo/.vscode/launch.json",
      );
    });

    it("maps an external debugger launch to the language-pack broker", async () => {
      (fileService.readFile as ReturnType<typeof vi.fn>).mockResolvedValue(`{
        "configurations": [{
          "name": "Python current file",
          "type": "debugpy",
          "request": "launch",
          "program": "${"${file}"}",
          "cwd": "${"${workspaceFolder}"}"
        }]
      }`);

      await loadLaunchConfigs("C:/repo");

      expect(debugState.launchConfigs).toEqual([
        expect.objectContaining({
          name: "Python current file",
          kind: "language-pack",
          adapterId: "debugpy",
          program: "",
          dir: "C:/repo",
        }),
      ]);
    });

    it("preserves the last valid launch configs when JSONC parsing fails", async () => {
      debugState.launchConfigs = [
        { name: "last-good", kind: "package", dir: "/repo" },
      ];
      (fileService.readFile as ReturnType<typeof vi.fn>).mockResolvedValue(
        "{ invalid",
      );

      await loadLaunchConfigs("/repo");

      expect(debugState.launchConfigs).toEqual([
        { name: "last-good", kind: "package", dir: "/repo" },
      ]);
    });

    it("maps common Go and Node attach configurations", async () => {
      (fileService.readFile as ReturnType<typeof vi.fn>).mockResolvedValue(`{
        "configurations": [
          {"name":"Go attach","type":"go","request":"attach","host":"localhost","port":2345},
          {"name":"Node attach","type":"node","request":"attach","port":9229}
        ]
      }`);

      await loadLaunchConfigs("/repo");

      expect(debugState.launchConfigs).toEqual([
        expect.objectContaining({
          name: "Go attach",
          kind: "package",
          request: "attach",
          address: "localhost:2345",
        }),
        expect.objectContaining({
          name: "Node attach",
          kind: "node",
          request: "attach",
          address: "127.0.0.1:9229",
        }),
      ]);
    });

    it("maps common Chrome and Edge launch and attach fields", async () => {
      (fileService.readFile as ReturnType<typeof vi.fn>).mockResolvedValue(`{
        "configurations": [
          {
            "name":"Chrome app",
            "type":"pwa-chrome",
            "request":"launch",
            "url":"http://localhost:5173/app",
            "webRoot":"${"${workspaceFolder}"}/web",
            "runtimeExecutable":"C:/Browsers/chrome.exe",
            "runtimeArgs":["--incognito"],
            "sourceMaps":true,
            "pathMapping":{"/src":"${"${workspaceFolder}"}/src"}
          },
          {
            "name":"Edge attach",
            "type":"pwa-msedge",
            "request":"attach",
            "host":"localhost",
            "port":9222,
            "targetId":"page-2"
          }
        ]
      }`);

      await loadLaunchConfigs("C:/repo");

      expect(debugState.launchConfigs).toEqual([
        expect.objectContaining({
          name: "Chrome app",
          kind: "browser",
          browser: "chrome",
          request: "launch",
          url: "http://localhost:5173/app",
          webRoot: "C:/repo/web",
          executablePath: "C:/Browsers/chrome.exe",
          runtimeArgs: ["--incognito"],
          sourceMaps: true,
          pathMappings: { "/src": "C:/repo/src" },
        }),
        expect.objectContaining({
          name: "Edge attach",
          kind: "browser",
          browser: "edge",
          request: "attach",
          address: "localhost:9222",
          targetId: "page-2",
        }),
      ]);
    });

    it("launches browser configs through the generated typed binding", async () => {
      (
        DebugServiceBindings.LaunchWithConfig as ReturnType<typeof vi.fn>
      ).mockResolvedValue({
        running: true,
        address: "127.0.0.1:9222",
        mode: "browser",
        message: "Chrome attached",
      });

      await launchWithConfig({
        name: "Chrome app",
        kind: "browser",
        dir: "C:/repo",
        browser: "chrome",
        request: "launch",
        url: "http://localhost:5173",
        executablePath: "C:/Browsers/chrome.exe",
        runtimeArgs: ["--incognito"],
        webRoot: "C:/repo/web",
        sourceMaps: true,
        pathMappings: { "/src": "C:/repo/src" },
      });

      expect(DebugServiceBindings.LaunchWithConfig).toHaveBeenCalledWith(
        expect.objectContaining({
          kind: "browser",
          browser: "chrome",
          request: "launch",
          url: "http://localhost:5173",
          executablePath: "C:/Browsers/chrome.exe",
          runtimeArgs: ["--incognito"],
        }),
      );
      expect(debugState.mode).toBe("browser");
      expect(debugState.activeConfigName).toBe("Chrome app");
    });

    it("runs a mapped Go attach configuration through the existing backend", async () => {
      (debugService.attachDelve as ReturnType<typeof vi.fn>).mockResolvedValue({
        running: true,
        address: "127.0.0.1:2345",
        mode: "debug",
        message: "attached",
      });

      await launchWithConfig({
        name: "Go attach",
        kind: "package",
        dir: "/repo",
        request: "attach",
        address: "127.0.0.1:2345",
      });

      expect(debugService.attachDelve).toHaveBeenCalledWith("127.0.0.1:2345");
      expect(debugState.activeConfigName).toBe("Go attach");
    });

    it("launches a compiled target by manifest adapter id without executable authority", async () => {
      (debugService.launchWithConfig as ReturnType<typeof vi.fn>).mockResolvedValue({
        running: true,
        address: "stdio",
        mode: "language-pack",
        message: "DAP session",
      });

      await launchWithConfig({
        name: "Rust binary",
        kind: "language-pack",
        adapterId: "lldb",
        dir: "C:/repo",
        program: "C:/repo/target/debug/example.exe",
      });

      expect(debugService.launchWithConfig).toHaveBeenCalledWith(
        expect.objectContaining({
          kind: "language-pack",
          adapterId: "lldb",
          program: "C:/repo/target/debug/example.exe",
        }),
      );
      expect(debugService.launchWithConfig).not.toHaveBeenCalledWith(
        expect.objectContaining({ executablePath: expect.anything() }),
      );
    });

    it("launches an external current file without renderer adapter authority", async () => {
      (
        debugService.launchWithConfig as ReturnType<typeof vi.fn>
      ).mockResolvedValue({
        running: true,
        address: "stdio",
        mode: "language-pack",
        message: "DAP session",
      });

      await launchCurrentFile("C:/repo/main.py", ["--fixture"]);

      expect(debugService.launchWithConfig).toHaveBeenCalledWith({
        name: "Language Pack: current file",
        kind: "language-pack",
        dir: "C:/repo",
        program: "C:/repo/main.py",
        args: ["--fixture"],
      });
      expect(debugState.mode).toBe("language-pack");
      expect(debugState.activeConfigName).toBe("Language Pack: current file");
      expect(debugService.launchWithConfig).not.toHaveBeenCalledWith(
        expect.objectContaining({ executablePath: expect.anything() }),
      );
    });
  });

  describe("browser targets and events", () => {
    it("selects a browser target through the generated binding", async () => {
      (
        DebugServiceBindings.SelectBrowserTarget as ReturnType<typeof vi.fn>
      ).mockResolvedValue(undefined);
      (debugService.getState as ReturnType<typeof vi.fn>).mockResolvedValue({
        session: {
          running: true,
          address: "127.0.0.1:9222",
          mode: "browser",
          message: "attached",
        },
        generation: 4,
        browserTargetId: "page-b",
        browserTargets: [
          { id: "page-b", type: "page", title: "B", url: "http://localhost/b" },
        ],
        browserConsole: [],
        browserNetwork: [],
      });

      await selectBrowserTarget("page-b");

      expect(DebugServiceBindings.SelectBrowserTarget).toHaveBeenCalledWith(
        "page-b",
      );
      expect(debugState.browserTargetId).toBe("page-b");
    });

    it("applies browser snapshots and replaces events on a new generation", () => {
      applyDebugSnapshot({
        session: {
          running: true,
          address: "127.0.0.1:9222",
          mode: "browser",
          message: "attached",
        },
        generation: 7,
        browserTargetId: "page-a",
        browserTargets: [
          { id: "page-a", type: "page", title: "A", url: "http://localhost/a" },
        ],
        browserConsole: [
          {
            generation: 7,
            level: "log",
            text: "ready",
            url: "",
            line: 0,
            timestamp: 1,
          },
        ],
        browserNetwork: [
          {
            generation: 7,
            requestId: "r1",
            phase: "response",
            method: "GET",
            url: "/api",
            status: 200,
            mimeType: "application/json",
            error: "",
            timestamp: 2,
          },
        ],
      });
      expect(debugState.browserTargetId).toBe("page-a");
      expect(debugState.browserConsole.map((entry) => entry.text)).toEqual([
        "ready",
      ]);
      expect(debugState.browserNetwork.map((entry) => entry.requestId)).toEqual(
        ["r1"],
      );

      applyDebugSnapshot({
        session: {
          running: true,
          address: "127.0.0.1:9333",
          mode: "browser",
          message: "restarted",
        },
        generation: 8,
        browserTargetId: "page-new",
        browserTargets: [
          {
            id: "page-new",
            type: "page",
            title: "New",
            url: "http://localhost/new",
          },
        ],
        browserConsole: [],
        browserNetwork: [],
      });
      expect(debugState.runGeneration).toBe(8);
      expect(debugState.browserTargetId).toBe("page-new");
      expect(debugState.browserConsole).toEqual([]);
      expect(debugState.browserNetwork).toEqual([]);
    });
  });

  describe("addFunctionBreakpoint", () => {
    it("rejects empty function name", async () => {
      await addFunctionBreakpoint("  ");
      expect(debugState.functionBreakpoints).toHaveLength(0);
      expect(debugService.setFunctionBreakpoints).not.toHaveBeenCalled();
    });

    it("adds to state and persists when name is valid", async () => {
      await addFunctionBreakpoint("main.main");
      expect(debugState.functionBreakpoints).toHaveLength(1);
      expect(debugState.functionBreakpoints[0].name).toBe("main.main");
      // localStorage 应被写入
      const raw = localStorage.getItem("koyori-ide.debug.functionBreakpoints");
      expect(raw).toContain("main.main");
      // 未运行时不应同步到后端
      expect(debugService.setFunctionBreakpoints).not.toHaveBeenCalled();
    });

    it("rejects duplicate function name", async () => {
      await addFunctionBreakpoint("pkg.Handle");
      await addFunctionBreakpoint("pkg.Handle");
      expect(debugState.functionBreakpoints).toHaveLength(1);
    });

    it("syncs to backend when running", async () => {
      debugState.running = true;
      await addFunctionBreakpoint("main.fn");
      expect(debugService.setFunctionBreakpoints).toHaveBeenCalledWith(
        debugState.functionBreakpoints,
      );
    });

    it("preserves condition and hitCondition when provided", async () => {
      await addFunctionBreakpoint("pkg.Do", "x > 0", ">=2");
      expect(debugState.functionBreakpoints[0]).toEqual({
        name: "pkg.Do",
        condition: "x > 0",
        hitCondition: ">=2",
      });
    });

    it("clears input fields after add", async () => {
      debugState.newFuncBpName = "fn";
      debugState.newFuncBpCondition = "x>0";
      debugState.newFuncBpHitCondition = ">=1";
      await addFunctionBreakpoint(
        debugState.newFuncBpName,
        debugState.newFuncBpCondition,
        debugState.newFuncBpHitCondition,
      );
      expect(debugState.newFuncBpName).toBe("");
      expect(debugState.newFuncBpCondition).toBe("");
      expect(debugState.newFuncBpHitCondition).toBe("");
    });
  });

  describe("removeFunctionBreakpoint", () => {
    it("removes from state by name", async () => {
      await addFunctionBreakpoint("a");
      await addFunctionBreakpoint("b");
      await removeFunctionBreakpoint("a");
      expect(debugState.functionBreakpoints).toHaveLength(1);
      expect(debugState.functionBreakpoints[0].name).toBe("b");
    });

    it("does not sync when running and list becomes empty", async () => {
      debugState.running = true;
      await addFunctionBreakpoint("a");
      (debugService.setFunctionBreakpoints as any).mockClear();
      await removeFunctionBreakpoint("a");
      // 后端要求至少 1 个断点 — 空列表不应调用
      expect(debugService.setFunctionBreakpoints).not.toHaveBeenCalled();
    });

    it("syncs when running and list still has entries", async () => {
      debugState.running = true;
      await addFunctionBreakpoint("a");
      await addFunctionBreakpoint("b");
      (debugService.setFunctionBreakpoints as any).mockClear();
      await removeFunctionBreakpoint("a");
      expect(debugService.setFunctionBreakpoints).toHaveBeenCalledWith(
        debugState.functionBreakpoints,
      );
    });
  });

  describe("applyFunctionBreakpoints", () => {
    it("errors when not running", async () => {
      await addFunctionBreakpoint("a");
      await applyFunctionBreakpoints();
      expect(debugService.setFunctionBreakpoints).not.toHaveBeenCalled();
    });

    it("errors when list is empty", async () => {
      debugState.running = true;
      await applyFunctionBreakpoints();
      expect(debugService.setFunctionBreakpoints).not.toHaveBeenCalled();
    });

    it("syncs to backend when running and list non-empty", async () => {
      debugState.running = true;
      await addFunctionBreakpoint("a");
      (debugService.setFunctionBreakpoints as any).mockClear();
      await applyFunctionBreakpoints();
      expect(debugService.setFunctionBreakpoints).toHaveBeenCalledWith(
        debugState.functionBreakpoints,
      );
    });
  });

  describe("setVariable", () => {
    it("errors when not running", async () => {
      await setVariable(10, "x", "42");
      expect(debugService.setVariable).not.toHaveBeenCalled();
    });

    it("errors with empty name", async () => {
      debugState.running = true;
      await setVariable(10, "", "42");
      expect(debugService.setVariable).not.toHaveBeenCalled();
    });

    it("calls backend, clears edit state, and refreshes locals on success", async () => {
      debugState.running = true;
      (debugService.setVariable as any).mockResolvedValue("42");
      // 模拟进入编辑模式
      startEditVariable(10, "x", "0");
      expect(debugState.editingVarName).toBe("x");

      await setVariable(10, "x", "42");

      expect(debugService.setVariable).toHaveBeenCalledWith(10, "x", "42");
      expect(debugState.editingVarName).toBe("");
      expect(debugState.editingVarValue).toBe("");
      expect(debugState.editingVarRef).toBe(0);
      // 应当刷新 locals
      expect(debugService.refreshStackAndLocals).toHaveBeenCalled();
    });
  });

  describe("startEditVariable / cancelEditVariable", () => {
    it("startEditVariable populates edit state", () => {
      startEditVariable(15, "y", "old");
      expect(debugState.editingVarRef).toBe(15);
      expect(debugState.editingVarName).toBe("y");
      expect(debugState.editingVarValue).toBe("old");
    });

    it("cancelEditVariable clears edit state", () => {
      startEditVariable(15, "y", "old");
      cancelEditVariable();
      expect(debugState.editingVarName).toBe("");
      expect(debugState.editingVarValue).toBe("");
      expect(debugState.editingVarRef).toBe(0);
    });
  });

  describe("restartFrame", () => {
    it("errors when not running", async () => {
      await restartFrame(1);
      expect(debugService.restartFrame).not.toHaveBeenCalled();
    });

    it("errors with invalid frameId", async () => {
      debugState.running = true;
      await restartFrame(0);
      expect(debugService.restartFrame).not.toHaveBeenCalled();
    });

    it("calls backend and refreshes on success", async () => {
      debugState.running = true;
      await restartFrame(5);
      expect(debugService.restartFrame).toHaveBeenCalledWith(5);
      expect(debugService.refreshStackAndLocals).toHaveBeenCalled();
    });
  });

  describe("refreshInlineValues", () => {
    it("clears state when not running", async () => {
      debugState.inlineValues = [{ type: "text", text: "stale" }];
      await refreshInlineValues(1, 10);
      expect(debugState.inlineValues).toHaveLength(0);
    });

    it("clears state when both refs are invalid", async () => {
      debugState.running = true;
      await refreshInlineValues(0, 0);
      expect(debugState.inlineValues).toHaveLength(0);
      expect(debugService.getInlineValues).not.toHaveBeenCalled();
    });

    it("populates state from backend on success", async () => {
      debugState.running = true;
      const mockList = [
        { type: "variable", name: "x", value: "42", variableReference: 0 },
        { type: "text", text: "inline-text" },
      ];
      (debugService.getInlineValues as any).mockResolvedValue(mockList);
      await refreshInlineValues(1, 10);
      expect(debugService.getInlineValues).toHaveBeenCalledWith(1, 10);
      expect(debugState.inlineValues).toEqual(mockList);
    });

    it("silently clears state on backend error", async () => {
      debugState.running = true;
      (debugService.getInlineValues as any).mockRejectedValue(
        new Error("not supported"),
      );
      debugState.inlineValues = [{ type: "text", text: "stale" }];
      await refreshInlineValues(1, 10);
      expect(debugState.inlineValues).toHaveLength(0);
    });
  });

  describe("refreshSessions", () => {
    it("populates sessions and activeSessionID on success", async () => {
      const mockList = [
        {
          id: "default",
          active: true,
          running: false,
          stopped: false,
          mode: "",
          address: "",
        },
        {
          id: "sess-1",
          active: false,
          running: true,
          stopped: true,
          mode: "debug",
          address: "127.0.0.1:1234",
        },
      ];
      (debugService.listSessions as any).mockResolvedValue(mockList);
      (debugService.getActiveSession as any).mockResolvedValue("default");
      await refreshSessions();
      expect(debugState.sessions).toEqual(mockList);
      expect(debugState.activeSessionID).toBe("default");
    });

    it("clears sessions list on backend error", async () => {
      (debugService.listSessions as any).mockRejectedValue(
        new Error("method missing"),
      );
      debugState.sessions = [
        {
          id: "stale",
          active: true,
          running: false,
          stopped: false,
          mode: "",
          address: "",
        },
      ];
      await refreshSessions();
      expect(debugState.sessions).toHaveLength(0);
    });
  });

  describe("switchSession", () => {
    it("no-op when id is empty", async () => {
      await switchSession("");
      expect(debugService.setActiveSession).not.toHaveBeenCalled();
    });

    it("no-op when id equals current active id", async () => {
      debugState.activeSessionID = "default";
      await switchSession("default");
      expect(debugService.setActiveSession).not.toHaveBeenCalled();
    });

    it("calls backend and updates state on success", async () => {
      debugState.activeSessionID = "default";
      (debugService.setActiveSession as any).mockResolvedValue(undefined);
      (debugService.getActiveSession as any).mockResolvedValue("sess-2");
      (debugService.listSessions as any).mockResolvedValue([]);
      await switchSession("sess-2");
      expect(debugService.setActiveSession).toHaveBeenCalledWith("sess-2");
      expect(debugState.activeSessionID).toBe("sess-2");
    });
  });

  describe("startDebugSession", () => {
    it("calls backend with proper config and returns session id", async () => {
      (debugService.startSession as any).mockResolvedValue("sess-42");
      (debugService.listSessions as any).mockResolvedValue([]);
      (debugService.getActiveSession as any).mockResolvedValue("sess-42");
      const id = await startDebugSession({
        name: "Go: Package",
        kind: "package",
        dir: "/proj",
        mode: "debug",
      });
      expect(debugService.startSession).toHaveBeenCalledWith({
        name: "Go: Package",
        kind: "package",
        dir: "/proj",
        program: "",
        runRegex: "",
        args: [],
        env: {},
        stopOnEntry: false,
        mode: "debug",
      });
      expect(id).toBe("sess-42");
      expect(debugState.activeConfigName).toBe("Go: Package");
    });

    it("returns null on backend error", async () => {
      (debugService.startSession as any).mockRejectedValue(
        new Error("launch failed"),
      );
      const id = await startDebugSession({
        name: "x",
        kind: "package",
        dir: "/proj",
      });
      expect(id).toBeNull();
    });
  });

  describe("stopDebugSessionByID", () => {
    it("errors when id is empty", async () => {
      await stopDebugSessionByID("");
      expect(debugService.stopSession).not.toHaveBeenCalled();
    });

    it("calls backend and refreshes state on success", async () => {
      (debugService.stopSession as any).mockResolvedValue(undefined);
      (debugService.listSessions as any).mockResolvedValue([]);
      (debugService.getActiveSession as any).mockResolvedValue("default");
      await stopDebugSessionByID("sess-1");
      expect(debugService.stopSession).toHaveBeenCalledWith("sess-1");
      expect(debugService.listSessions).toHaveBeenCalled();
    });

    it("stops polling after the final debug session exits", async () => {
      vi.useFakeTimers();
      debugState.running = true;
      initDebugRuntime();
      expect(debugState.pollTimer).not.toBe(0);

      (debugService.stopSession as ReturnType<typeof vi.fn>).mockResolvedValue(
        undefined,
      );
      (debugService.listSessions as ReturnType<typeof vi.fn>).mockResolvedValue(
        [],
      );
      (
        debugService.getActiveSession as ReturnType<typeof vi.fn>
      ).mockResolvedValue("");
      (debugService.getState as ReturnType<typeof vi.fn>).mockResolvedValue({
        session: {
          running: false,
          stopped: false,
          address: "",
          mode: "",
          message: "stopped",
        },
        breakpoints: [],
        stack: [],
        locals: [],
        watches: [],
      });

      await stopDebugSessionByID("sess-1");

      expect(debugState.pollTimer).toBe(0);
      expect(vi.getTimerCount()).toBe(0);
    });
  });
});

describe("G14: DAP variablesReference 真实保留与嵌套展开", () => {
  it("fetchVariables rejects invalid references without calling the adapter", async () => {
    const before = vi.mocked(DebugServiceBindings.GetVariables).mock.calls
      .length;
    await expect(fetchVariables(0)).resolves.toEqual([]);
    await expect(fetchVariables(-1)).resolves.toEqual([]);
    expect(vi.mocked(DebugServiceBindings.GetVariables).mock.calls.length).toBe(
      before,
    );
  });

  it("fetchVariables forwards the adapter-owned reference with paging", async () => {
    vi.mocked(DebugServiceBindings.GetVariables).mockResolvedValue([
      { name: "y", value: "7", type: "int", variablesReference: 0 },
      {
        name: "inner",
        value: "{...}",
        type: "struct",
        variablesReference: 202,
      },
    ]);
    debugState.running = true;
    const children = await fetchVariables(101, 5, 20);
    expect(vi.mocked(DebugServiceBindings.GetVariables)).toHaveBeenCalledWith(
      101,
      5,
      20,
    );
    expect(children).toHaveLength(2);
    expect(children[1].variablesReference).toBe(202);
  });

  it("toggleVariableExpansion expands and collapses a nested variable", async () => {
    vi.mocked(DebugServiceBindings.GetVariables).mockResolvedValue([
      { name: "y", value: "7", type: "int", variablesReference: 0 },
    ]);
    debugState.running = true;
    const v = {
      name: "obj",
      value: "{...}",
      type: "struct",
      variablesReference: 101,
    };

    await toggleVariableExpansion(v);
    expect(debugState.expandedVariables[101]).toHaveLength(1);

    await toggleVariableExpansion(v);
    expect(debugState.expandedVariables[101]).toBeUndefined();
  });

  it("clearExpandedVariables resets all expanded state", async () => {
    debugState.expandedVariables = { 101: [], 202: [] };
    clearExpandedVariables();
    expect(debugState.expandedVariables).toEqual({});
  });

  it("applyDebugSnapshot preserves the adapter-owned variablesReference in locals", () => {
    debugState.running = true;
    debugState.stopped = true;
    applyDebugSnapshot({
      session: {
        running: true,
        stopped: true,
        address: "",
        mode: "",
        message: "",
      },
      breakpoints: [],
      stack: [],
      locals: [
        { name: "x", value: "42", type: "int", variablesReference: 0 },
        {
          name: "obj",
          value: "{...}",
          type: "struct",
          variablesReference: 101,
        },
      ],
      watches: [],
      stopReason: "entry",
      runGeneration: 1,
    } as never);
    const obj = debugState.locals.find((l) => l.name === "obj");
    expect(obj?.variablesReference).toBe(101);
  });
});
