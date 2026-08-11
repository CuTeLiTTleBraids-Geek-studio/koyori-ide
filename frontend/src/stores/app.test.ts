import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { effect, stop } from "vue";

vi.mock("@/lib/monaco-themes", () => ({
  accentThemes: {
    blue: { label: "Blue", color: "#4285f4", monacoTheme: "koyoriIde-blue", monacoLightTheme: "koyoriIde-light-blue" },
  },
  applyMonacoTheme: vi.fn(),
  applyMonacoThemeForMode: vi.fn(),
  registerAllThemes: vi.fn(),
}));

vi.mock("@/api/services", () => ({
  settingsService: {
    loadSettings: vi.fn((...args: unknown[]) => (
      globalThis as typeof globalThis & {
        __koyoriIdeSettingsServiceTestBinding?: typeof settingsServiceMock;
      }
    ).__koyoriIdeSettingsServiceTestBinding?.loadSettings(...args)),
    saveSettings: vi.fn((...args: unknown[]) => (
      globalThis as typeof globalThis & {
        __koyoriIdeSettingsServiceTestBinding?: typeof settingsServiceMock;
      }
    ).__koyoriIdeSettingsServiceTestBinding?.saveSettings(...args)),
  },
  // Priority 4: fileService.readFile 供 loadWorkspaceFolders 使用。
  fileService: {
    readFile: wsReadFileMock,
  },
  projectService: {
    addProject: addProjectMock,
    addMultiRootProject: addMultiRootProjectMock,
    getWorkspaceSnapshot: getWorkspaceSnapshotMock,
  },
}));

// Priority 4: fileService.readFile mock 用于 loadWorkspaceFolders / openProject
// 的 .code-workspace 解析测试。hoisted 以便 vi.mock 工厂引用。
const { wsReadFileMock, settingsServiceMock, addProjectMock, addMultiRootProjectMock, getWorkspaceSnapshotMock } = vi.hoisted(() => ({
  wsReadFileMock: vi.fn(),
  addProjectMock: vi.fn(),
  addMultiRootProjectMock: vi.fn(),
  getWorkspaceSnapshotMock: vi.fn(),
  settingsServiceMock: {
    loadSettings: vi.fn().mockResolvedValue({}),
    saveSettings: vi.fn().mockResolvedValue(undefined),
  },
}));

vi.mock("@wailsio/runtime", () => ({
  Events: {
    On: vi.fn(),
    Emit: vi.fn(),
  },
}));

// Priority 4: openProject 动态导入 snapshot/workflows，mock 避免副作用。
vi.mock("@/stores/snapshot", () => ({
  setSnapshotWorkspaceRoot: vi.fn(),
}));
vi.mock("@/stores/workflows", () => ({
  loadWorkflows: vi.fn().mockResolvedValue(undefined),
}));

const settingsService = settingsServiceMock;
import {
  appState,
  themeStore,
  settingsStore,
  aiConfigStore,
  projectStore,
  windowStore,
  applyMode,
  resolveSystemMode,
  saveSettings,
  flushSettingsSave,
  initSettingsSyncListener,
  handleSettingsChangedEvent,
  initProjectRemovedListener,
  handleProjectRemovedEvent,
  startSystemModeListener,
  unregisterAppListeners,
  // Priority 4: 多根工作区 Workspace Folders 解析工具
  isCodeWorkspacePath,
  parseCodeWorkspaceContent,
  loadWorkspaceFolders,
  openProject,
  resetWorkspaceAuthorityForTesting,
} from "./app";
import type { AppState } from "./app";

beforeEach(() => {
  (
    globalThis as typeof globalThis & {
      __koyoriIdeSettingsServiceTestBinding?: typeof settingsServiceMock;
    }
  ).__koyoriIdeSettingsServiceTestBinding = settingsServiceMock;
});

afterEach(() => {
  delete (
    globalThis as typeof globalThis & {
      __koyoriIdeSettingsServiceTestBinding?: typeof settingsServiceMock;
    }
  ).__koyoriIdeSettingsServiceTestBinding;
});

describe("Theme Mode", () => {
  beforeEach(() => {
    document.documentElement.removeAttribute("data-mode");
    appState.theme = "dark";
    appState.accentTheme = "blue";
  });

  it("resolveSystemMode returns 'dark' or 'light'", () => {
    const mode = resolveSystemMode();
    expect(["dark", "light"]).toContain(mode);
  });

  it("applyMode('dark') sets data-mode to dark", () => {
    applyMode("dark");
    expect(document.documentElement.getAttribute("data-mode")).toBe("dark");
  });

  it("applyMode('light') sets data-mode to light", () => {
    applyMode("light");
    expect(document.documentElement.getAttribute("data-mode")).toBe("light");
  });

  it("applyMode('system') sets data-mode to resolved system mode", () => {
    applyMode("system");
    const resolved = resolveSystemMode();
    expect(document.documentElement.getAttribute("data-mode")).toBe(resolved);
  });

  it("applyMode updates appState.theme", () => {
    applyMode("light");
    expect(appState.theme).toBe("light");
  });
});

describe("AI window preferences", () => {
  it("persists theme and dock widths independently", async () => {
    vi.useFakeTimers();
    vi.mocked(settingsService.saveSettings).mockClear();
    appState.aiWindowTheme = "claude-light";
    appState.aiSidebarWidth = 336;
    appState.aiTerminalWidth = 520;

    saveSettings();
    await vi.advanceTimersByTimeAsync(600);

    expect(settingsService.saveSettings).toHaveBeenCalledWith(expect.objectContaining({
      aiWindowTheme: "claude-light",
      aiSidebarWidth: 336,
      aiTerminalWidth: 520,
    }));
    vi.useRealTimers();
  });
});

describe("settings CAS", () => {

  it("flushSettingsSave persists immediately without waiting for the debounce", async () => {
    vi.useFakeTimers();
    vi.mocked(settingsService.saveSettings).mockClear();
    appState.settingsVersion = 3;
    saveSettings();
    expect(settingsService.saveSettings).not.toHaveBeenCalled();
    await flushSettingsSave();
    expect(settingsService.saveSettings).toHaveBeenCalledTimes(1);
    expect(settingsService.saveSettings).toHaveBeenCalledWith(expect.objectContaining({
      expectedVersion: 3,
    }));
    expect(appState.settingsVersion).toBe(4);
    appState.settingsVersion = 0;
    vi.useRealTimers();
  });

  it("flushSettingsSave is a no-op when no save is pending", async () => {
    vi.mocked(settingsService.saveSettings).mockClear();
    await flushSettingsSave();
    expect(settingsService.saveSettings).not.toHaveBeenCalled();
  });
  it("sends expectedVersion and reloads the SSOT after a version conflict", async () => {
    vi.useFakeTimers();
    appState.settingsVersion = 7;
    vi.mocked(settingsService.saveSettings).mockRejectedValueOnce(
      new Error("settings version conflict"),
    );
    vi.mocked(settingsService.loadSettings).mockClear();

    saveSettings();
    await vi.advanceTimersByTimeAsync(600);

    expect(settingsService.saveSettings).toHaveBeenCalledWith(
      expect.objectContaining({ expectedVersion: 7, version: 7 }),
    );
    expect(settingsService.loadSettings).toHaveBeenCalledTimes(1);
    appState.fontSize = 14;
    vi.useRealTimers();
  });
});

describe("Emmet settings", () => {
  it("persists the enable switch and includeLanguages mapping", async () => {
    vi.useFakeTimers();
    vi.mocked(settingsService.saveSettings).mockClear();
    appState.emmetEnabled = false;
    appState.emmetIncludeLanguages = { templ: "html" };

    saveSettings();
    await vi.advanceTimersByTimeAsync(600);

    expect(settingsService.saveSettings).toHaveBeenCalledWith(expect.objectContaining({
      emmetEnabled: false,
      emmetIncludeLanguages: { templ: "html" },
    }));
    appState.emmetEnabled = true;
    appState.emmetIncludeLanguages = {};
    vi.useRealTimers();
  });
});

describe("H-13: app 监听器可重入与统一注销", () => {
  it("startSystemModeListener 重复调用只注册一个 media listener", () => {
    const addEventListener = vi.fn();
    const removeEventListener = vi.fn();
    vi.stubGlobal("matchMedia", vi.fn(() => ({
      matches: false,
      addEventListener,
      removeEventListener,
    })));

    startSystemModeListener();
    startSystemModeListener();
    startSystemModeListener();

    expect(addEventListener).toHaveBeenCalledTimes(1);
    unregisterAppListeners();
    expect(removeEventListener).toHaveBeenCalledTimes(1);
    vi.unstubAllGlobals();
  });

  it("initSettingsSyncListener 只启用处理器，不直接注册 Wails 监听器", async () => {
    const { Events } = await import("@wailsio/runtime");
    initSettingsSyncListener();
    initSettingsSyncListener();
    initSettingsSyncListener();
    expect(Events.On).not.toHaveBeenCalled();
  });

  it("initProjectRemovedListener 只启用处理器，不直接注册 Wails 监听器", async () => {
    const { Events } = await import("@wailsio/runtime");
    initProjectRemovedListener();
    initProjectRemovedListener();
    expect(Events.On).not.toHaveBeenCalled();
  });

  it("unregisterAppListeners 使 settings/project 旧回调失效", async () => {
    vi.mocked(settingsService.loadSettings).mockClear();
    initSettingsSyncListener();
    initProjectRemovedListener();
    unregisterAppListeners();
    handleSettingsChangedEvent({ data: { origin: "peer" } });
    handleProjectRemovedEvent({ data: { path: "/project" } });
    await Promise.resolve();
    expect(settingsService.loadSettings).not.toHaveBeenCalled();
  });

  it("unregisterAppListeners flushes a pending settings save (P9-G11)", async () => {
    vi.useFakeTimers();
    vi.mocked(settingsService.saveSettings).mockClear();

    saveSettings();
    unregisterAppListeners();
    await vi.advanceTimersByTimeAsync(600);

    expect(settingsService.saveSettings).toHaveBeenCalledTimes(1);
    appState.settingsVersion = 0;
  });
});

describe("H-18: appState sub-store delegation", () => {
  it("sub-stores are independent", () => {
    // Write to themeStore — appState mirrors it, settingsStore is untouched.
    themeStore.theme = "light";
    expect(appState.theme).toBe("light");
    expect(settingsStore.fontSize).toBe(14);

    // Write to settingsStore — appState mirrors it, themeStore is untouched.
    settingsStore.fontSize = 20;
    expect(appState.fontSize).toBe(20);
    expect(themeStore.theme).toBe("light");

    // Restore defaults so subsequent tests start clean.
    themeStore.theme = "dark";
    settingsStore.fontSize = 14;
  });

  it("appState write delegates to the owning sub-store", () => {
    appState.theme = "light";
    expect(themeStore.theme).toBe("light");

    appState.fontSize = 20;
    expect(settingsStore.fontSize).toBe(20);

    // Restore.
    themeStore.theme = "dark";
    settingsStore.fontSize = 14;
  });

  it("reactivity propagates through appState when a sub-store changes", () => {
    let runs = 0;
    let last: string | undefined;
    const runner = effect(() => {
      last = appState.theme;
      runs++;
    });
    expect(runs).toBe(1);

    const before = runs;
    themeStore.theme = "light";
    expect(runs).toBe(before + 1);
    expect(last).toBe("light");
    expect(appState.theme).toBe("light");

    stop(runner);
    // Restore.
    themeStore.theme = "dark";
  });

  it("representative AppState fields are present on appState and read from sub-stores", () => {
    // ~10 fields spanning all 5 sub-stores.
    const checks: Array<[keyof AppState, unknown]> = [
      ["theme", themeStore.theme],
      ["uiDensity", themeStore.uiDensity],
      ["fontSize", settingsStore.fontSize],
      ["scrollback", settingsStore.scrollback],
      ["aiModel", aiConfigStore.aiModel],
      ["aiProviderConfigs", aiConfigStore.aiProviderConfigs],
      ["currentProject", projectStore.currentProject],
      ["personalization", projectStore.personalization],
      ["sidebarCollapsed", windowStore.sidebarCollapsed],
      ["panelTab", windowStore.panelTab],
    ];
    for (const [field, expected] of checks) {
      expect(field in appState).toBe(true);
      expect(appState[field]).toBe(expected);
    }
  });

  it("sub-stores are exported and reactive", () => {
    const cases = [
      { store: themeStore, field: "uiDensity" as const, val: "compact" },
      { store: settingsStore, field: "minimap" as const, val: !settingsStore.minimap },
      { store: aiConfigStore, field: "aiProvider" as const, val: "anthropic" },
      { store: projectStore, field: "cursorLine" as const, val: 42 },
      { store: windowStore, field: "sidebarWidth" as const, val: 300 },
    ] as const;

    for (const c of cases) {
      const target = c.store as Record<string, unknown>;
      const original = target[c.field];
      let count = 0;
      const runner = effect(() => {
        void target[c.field];
        count++;
      });
      expect(count).toBe(1);
      target[c.field] = c.val;
      expect(count).toBe(2);
      expect(target[c.field]).toBe(c.val);
      stop(runner);
      // Restore.
      target[c.field] = original;
    }
  });
});

// ============================================================================
// Priority 4 (prompt-1.md): 多根工作区 Workspace Folders 前端解析测试
// 对应后端 services TestProjectService_P4_CodeWorkspaceFile / TestLSPService_P4_*
// ============================================================================

describe("P4: 多根工作区 Workspace Folders", () => {
  beforeEach(() => {
    // 每个测试前重置状态与 mock，避免相互干扰。
    projectStore.workspaceFolders = [];
    projectStore.currentProject = null;
    projectStore.projectName = null;
    projectStore.workspaceRoot = null;
    projectStore.workspaceGeneration = 0;
    wsReadFileMock.mockReset();
    addProjectMock.mockReset();
    addMultiRootProjectMock.mockReset();
    getWorkspaceSnapshotMock.mockReset();
    resetWorkspaceAuthorityForTesting();
    addProjectMock.mockResolvedValue({
      id: "project-id",
      name: "proj",
      path: "/home/user/proj",
      createdAt: 1,
      lastOpened: 1,
      exists: true,
    });
    addMultiRootProjectMock.mockResolvedValue({
      id: "workspace-id",
      name: "workspace",
      path: "/proj/myws.code-workspace",
      roots: ["/proj/frontend", "/proj/backend"],
      isWorkspace: true,
      createdAt: 1,
      lastOpened: 1,
      exists: true,
    });
  });

  describe("isCodeWorkspacePath", () => {
    it("识别 .code-workspace 后缀", () => {
      expect(isCodeWorkspacePath("/home/user/proj.code-workspace")).toBe(true);
    });

    it("大小写不敏感", () => {
      expect(isCodeWorkspacePath("PROJ.CODE-WORKSPACE")).toBe(true);
      expect(isCodeWorkspacePath("Proj.Code-Workspace")).toBe(true);
    });

    it("非 .code-workspace 文件返回 false", () => {
      expect(isCodeWorkspacePath("/home/user/proj.json")).toBe(false);
      expect(isCodeWorkspacePath("workspace.txt")).toBe(false);
    });

    it("空字符串返回 false", () => {
      expect(isCodeWorkspacePath("")).toBe(false);
    });
  });

  describe("parseCodeWorkspaceContent", () => {
    it("解析相对路径并以 baseDir 为基准拼接", () => {
      const content = JSON.stringify({
        folders: [{ path: "frontend" }, { path: "backend" }],
      });
      const result = parseCodeWorkspaceContent(content, "/home/user/proj");
      expect(result).toEqual(["/home/user/proj/frontend", "/home/user/proj/backend"]);
    });

    it("保留绝对路径", () => {
      const content = JSON.stringify({
        folders: [{ path: "/abs/root-a" }, { path: "/abs/root-b" }],
      });
      const result = parseCodeWorkspaceContent(content, "/home/user/proj");
      expect(result).toEqual(["/abs/root-a", "/abs/root-b"]);
    });

    it("解析 file:// URI（Windows 盘符形式）", () => {
      const content = JSON.stringify({
        folders: [{ uri: "file:///C:/Users/proj/frontend" }],
      });
      const result = parseCodeWorkspaceContent(content, "/home/user/proj");
      expect(result).toEqual(["C:/Users/proj/frontend"]);
    });

    it("folder 缺失 path 时回退到 uri 字段", () => {
      const content = JSON.stringify({
        folders: [{ uri: "file:///C:/proj/a" }, { path: "b" }],
      });
      const result = parseCodeWorkspaceContent(content, "/base");
      expect(result).toEqual(["C:/proj/a", "/base/b"]);
    });

    it("去重相同解析路径", () => {
      const content = JSON.stringify({
        folders: [{ path: "frontend" }, { path: "frontend" }, { path: "backend" }],
      });
      const result = parseCodeWorkspaceContent(content, "/home/user/proj");
      expect(result).toEqual(["/home/user/proj/frontend", "/home/user/proj/backend"]);
    });

    it("忽略 settings 等非 folders 字段", () => {
      const content = JSON.stringify({
        folders: [{ path: "a" }],
        settings: { "editor.fontSize": 14 },
        extensions: { recommendations: ["foo"] },
      });
      const result = parseCodeWorkspaceContent(content, "/base");
      expect(result).toEqual(["/base/a"]);
    });

    it("空内容抛出错误", () => {
      expect(() => parseCodeWorkspaceContent("   ", "/base")).toThrow(/empty/);
    });

    it("无效 JSON 抛出错误", () => {
      expect(() => parseCodeWorkspaceContent("{not json}", "/base")).toThrow(/parse code-workspace JSON/);
    });

    it("缺失 folders 数组抛出错误", () => {
      expect(() =>
        parseCodeWorkspaceContent(JSON.stringify({ settings: {} }), "/base"),
      ).toThrow(/missing 'folders'/);
    });

    it("folder 缺失 path 和 uri 抛出错误", () => {
      const content = JSON.stringify({ folders: [{ name: "no-path" }] });
      expect(() => parseCodeWorkspaceContent(content, "/base")).toThrow(/missing path\/uri/);
    });

    it("Windows 反斜杠相对路径规范化为正斜杠", () => {
      const content = JSON.stringify({
        folders: [{ path: "sub\\dir" }],
      });
      const result = parseCodeWorkspaceContent(content, "C:/proj");
      expect(result).toEqual(["C:/proj/sub/dir"]);
    });
  });

  describe("loadWorkspaceFolders", () => {
    it("读取并解析 .code-workspace 文件", async () => {
      wsReadFileMock.mockResolvedValue(
        JSON.stringify({ folders: [{ path: "frontend" }, { path: "backend" }] }),
      );
      // workspacePath = /proj/myws.code-workspace → baseDir = /proj
      const result = await loadWorkspaceFolders("/proj/myws.code-workspace");
      expect(result).toEqual(["/proj/frontend", "/proj/backend"]);
    });

    it("读取失败时拒绝而不是把 workspace 文件冒充目录根", async () => {
      wsReadFileMock.mockRejectedValue(new Error("IO error"));
      await expect(loadWorkspaceFolders("/proj/myws.code-workspace")).rejects.toThrow("IO error");
    });
  });

  describe("openProject", () => {
    it("单根项目：workspaceFolders 设为 [path]", () => {
      getWorkspaceSnapshotMock.mockResolvedValue({
        root: "/home/user/proj",
        roots: ["/home/user/proj"],
        generation: 1,
        projectId: "project-id",
        projectName: "my-project",
        projectPath: "/home/user/proj",
      });
      return openProject("my-project", "/home/user/proj").then(() => {
        expect(appState.workspaceFolders).toEqual(["/home/user/proj"]);
        expect(appState.currentProject).toBe("/home/user/proj");
      });
    });

    it("code-workspace 项目：由后端统一多根入口验证后原子回填", async () => {
      getWorkspaceSnapshotMock.mockResolvedValue({
        root: "/proj/frontend",
        roots: ["/proj/frontend", "/proj/backend"],
        generation: 1,
        projectId: "workspace-id",
        projectName: "workspace",
        projectPath: "/proj/myws.code-workspace",
      });
      await openProject("workspace", "/proj/myws.code-workspace");

      expect(addMultiRootProjectMock).toHaveBeenCalledWith([], "/proj/myws.code-workspace");
      expect(appState.currentProject).toBe("/proj/myws.code-workspace");
      expect(appState.projectName).toBe("workspace");
      expect(appState.workspaceFolders).toEqual(["/proj/frontend", "/proj/backend"]);
    });

    it("code-workspace 任一根无效时保持上一工作区完整状态", async () => {
      appState.currentProject = "/existing";
      appState.projectName = "existing";
      appState.workspaceFolders = ["/existing"];
      addMultiRootProjectMock.mockRejectedValue(new Error("workspace root is not accessible"));

      await expect(openProject("workspace", "/proj/myws.code-workspace")).rejects.toThrow(
        "workspace root is not accessible",
      );
      expect(appState.currentProject).toBe("/existing");
      expect(appState.projectName).toBe("existing");
      expect(appState.workspaceFolders).toEqual(["/existing"]);
    });
  });
});

