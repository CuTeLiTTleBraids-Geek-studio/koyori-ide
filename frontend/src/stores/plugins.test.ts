/**
 * plugins store 的单元测试（Plan 49）。
 *
 * 测试策略：
 * - 用 vi.hoisted 定义 mock 函数与数据，使 vi.mock 工厂能引用它们。
 * - mock @/api/services 提供 pluginService（任务要求）。
 * - mock @/lib/pluginRegistry 提供 syncPlugins / activateOnStartup /
 *   activatePlugin / deactivatePlugin / enablePlugin / disablePlugin /
 *   listPluginStates，将 store 与注册表的动态 import / 沙箱逻辑隔离，
 *   专注验证 store 的编排行为。
 * - mock @/stores/app 提供 appState，既断开 Monaco 导入链，又能控制
 *   currentProject 的值。
 */
import { describe, it, expect, beforeEach, vi } from "vitest";
import type { PluginInfo, PluginManifest } from "@/types";

// 用 vi.hoisted 提前定义 mock，保证 vi.mock 工厂（会被提升）能引用到。
const mocks = vi.hoisted(() => ({
  // pluginService 的 mock 方法
  listPlugins: vi.fn(),
  setPluginEnabled: vi.fn(),
  getPlugin: vi.fn(),
  readPluginFile: vi.fn(),
  // pluginRegistry 的 mock 方法
  syncPlugins: vi.fn(),
  activateOnStartup: vi.fn().mockResolvedValue(["git-helper"]),
  activatePlugin: vi.fn().mockResolvedValue(undefined),
  deactivatePlugin: vi.fn().mockResolvedValue(undefined),
  enablePlugin: vi.fn().mockResolvedValue(undefined),
  disablePlugin: vi.fn().mockResolvedValue(undefined),
  listPluginStates: vi.fn().mockReturnValue([]),
  unregisterPluginContributions: vi.fn(),
  // appState mock：普通对象即可，store 只读取 currentProject
  appState: { currentProject: "/home/user/my-project" },
}));

vi.mock("@/api/services", () => ({
  pluginService: {
    listPlugins: mocks.listPlugins,
    setPluginEnabled: mocks.setPluginEnabled,
    getPlugin: mocks.getPlugin,
    readPluginFile: mocks.readPluginFile,
  },
}));

vi.mock("@/lib/pluginRegistry", () => ({
  syncPlugins: mocks.syncPlugins,
  activateOnStartup: mocks.activateOnStartup,
  activatePlugin: mocks.activatePlugin,
  deactivatePlugin: mocks.deactivatePlugin,
  enablePlugin: mocks.enablePlugin,
  disablePlugin: mocks.disablePlugin,
  listPluginStates: mocks.listPluginStates,
  unregisterPluginContributions: mocks.unregisterPluginContributions,
}));

vi.mock("@/stores/app", () => ({
  appState: mocks.appState,
}));

import {
  pluginStore,
  installedPlugins,
  pluginActivations,
  isLoadingPlugins,
  pluginLoadError,
  loadPlugins,
  activateStartupPlugins,
  togglePluginEnabled,
  activatePluginByName,
  deactivatePluginByName,
  retryPluginActivation,
  reloadPlugins,
} from "./plugins";

// ---------------------------------------------------------------------------
// 测试数据构造辅助函数
// ---------------------------------------------------------------------------

function makeManifest(overrides: Partial<PluginManifest> = {}): PluginManifest {
  return {
    name: "git-helper",
    version: "1.2.0",
    main: "main.js",
    description: "A Git helper plugin",
    author: "koyori-ide",
    license: "MIT",
    activationEvents: ["onStartup"],
    ...overrides,
  };
}

function makePluginInfo(
  manifestOverrides: Partial<PluginManifest> = {},
  infoOverrides: Partial<PluginInfo> = {},
): PluginInfo {
  const manifest = makeManifest(manifestOverrides);
  return {
    manifest,
    path: `/plugins/${manifest.name}`,
    source: "user",
    enabled: true,
    mainExists: true,
    ...infoOverrides,
  };
}

// 两个真实合理的插件，用于多个测试用例
const pluginGitHelper = makePluginInfo({
  name: "git-helper",
  description: "Provides Git status badges and commands",
  activationEvents: ["onStartup"],
  permissions: ["fs.read", "shell.exec"],
});

const pluginMarkdownPreview = makePluginInfo({
  name: "markdown-preview",
  version: "0.4.1",
  main: "index.js",
  description: "Live Markdown preview in the panel",
  activationEvents: ["onCommand:markdown.preview"],
  permissions: ["fs.read"],
}, {
  source: "project",
  enabled: false,
  mainExists: true,
});

describe("plugins store", () => {
  beforeEach(() => {
    // 重置 store 状态
    pluginStore.plugins = [];
    pluginStore.activations = [];
    pluginStore.loading = false;
    pluginStore.error = null;
    // 重置 appState.currentProject 为默认值
    mocks.appState.currentProject = "/home/user/my-project";
    // 清除 mock 调用记录，保留 hoisted 中设定的默认实现
    vi.clearAllMocks();
  });

  // 1. 初始状态
  it("初始状态为空且未在加载", () => {
    expect(pluginStore.plugins).toHaveLength(0);
    expect(pluginStore.activations).toHaveLength(0);
    expect(pluginStore.loading).toBe(false);
    expect(pluginStore.error).toBeNull();
  });

  // 2. 计算属性 getters 反映初始状态
  it("getters 反映初始状态", () => {
    expect(installedPlugins.value).toEqual([]);
    expect(pluginActivations.value).toEqual([]);
    expect(isLoadingPlugins.value).toBe(false);
    expect(pluginLoadError.value).toBeNull();
  });

  // 3. loadPlugins 从后端加载插件列表并填充 store
  it("loadPlugins 调用 pluginService.listPlugins 并填充 plugins", async () => {
    mocks.listPlugins.mockResolvedValue([pluginGitHelper, pluginMarkdownPreview]);

    await loadPlugins();

    expect(mocks.listPlugins).toHaveBeenCalledWith("/home/user/my-project");
    expect(pluginStore.plugins).toHaveLength(2);
    expect(pluginStore.plugins[0].manifest.name).toBe("git-helper");
    expect(pluginStore.plugins[1].manifest.name).toBe("markdown-preview");
    expect(pluginStore.loading).toBe(false);
    expect(pluginStore.error).toBeNull();
  });

  // 4. loadPlugins 把后端返回的插件列表传给 syncPlugins 同步注册表
  it("loadPlugins 把插件列表传给 syncPlugins", async () => {
    const list = [pluginGitHelper];
    mocks.listPlugins.mockResolvedValue(list);

    await loadPlugins();

    expect(mocks.syncPlugins).toHaveBeenCalledTimes(1);
    expect(mocks.syncPlugins).toHaveBeenCalledWith(list);
  });

  // 5. loadPlugins 期间 loading 标志为 true，完成后恢复 false
  it("loadPlugins 期间 loading 为 true，完成后为 false", async () => {
    let loadingDuringCall = false;
    mocks.listPlugins.mockImplementation(async () => {
      // 在异步等待期间检查 loading 状态
      loadingDuringCall = pluginStore.loading;
      return [pluginGitHelper];
    });

    await loadPlugins();

    expect(loadingDuringCall).toBe(true);
    expect(pluginStore.loading).toBe(false);
  });

  // 6. loadPlugins 使用 appState.currentProject 作为 projectRoot
  it("loadPlugins 使用 appState.currentProject 作为 projectRoot", async () => {
    mocks.appState.currentProject = "/workspace/custom-repo";
    mocks.listPlugins.mockResolvedValue([]);

    await loadPlugins();

    expect(mocks.listPlugins).toHaveBeenCalledWith("/workspace/custom-repo");
  });

  // 7. loadPlugins 完成后用 listPluginStates 刷新 activations
  it("loadPlugins 完成后用 listPluginStates 刷新 activations", async () => {
    const states = [
      { name: "git-helper", status: "loaded" as const },
      { name: "markdown-preview", status: "disabled" as const },
    ];
    mocks.listPlugins.mockResolvedValue([pluginGitHelper, pluginMarkdownPreview]);
    mocks.listPluginStates.mockReturnValue(states);

    await loadPlugins();

    expect(mocks.listPluginStates).toHaveBeenCalled();
    expect(pluginStore.activations).toEqual(states);
  });

  // 8. loadPlugins 出错时设置 error 并保证 loading 为 false
  it("loadPlugins 出错时记录错误并重置 loading", async () => {
    mocks.listPlugins.mockRejectedValue(new Error("backend unavailable"));

    await loadPlugins();

    expect(pluginStore.error).toBe("backend unavailable");
    expect(pluginStore.loading).toBe(false);
    expect(pluginStore.plugins).toHaveLength(0);
    // 出错时不应同步注册表
    expect(mocks.syncPlugins).not.toHaveBeenCalled();
  });

  // 9. activateStartupPlugins 调用 activateOnStartup 并刷新 activations
  it("activateStartupPlugins 调用 activateOnStartup 并刷新 activations", async () => {
    const states = [{ name: "git-helper", status: "activated" as const }];
    mocks.listPluginStates.mockReturnValue(states);

    await activateStartupPlugins();

    expect(mocks.activateOnStartup).toHaveBeenCalledTimes(1);
    expect(pluginStore.activations).toEqual(states);
  });

  // 10. togglePluginEnabled(true) 持久化、启用插件并刷新列表
  it("togglePluginEnabled(true) 调用 setPluginEnabled 与 enablePlugin 并刷新列表", async () => {
    const enabledInfo = { ...pluginGitHelper, enabled: true };
    mocks.listPlugins.mockResolvedValue([enabledInfo]);
    const states = [{ name: "git-helper", status: "activated" as const }];
    mocks.listPluginStates.mockReturnValue(states);

    await togglePluginEnabled("git-helper", true);

    expect(mocks.setPluginEnabled).toHaveBeenCalledWith("git-helper", true);
    expect(mocks.enablePlugin).toHaveBeenCalledWith("git-helper");
    expect(mocks.disablePlugin).not.toHaveBeenCalled();
    // 刷新时再次调用 listPlugins（用 currentProject）
    expect(mocks.listPlugins).toHaveBeenCalledWith("/home/user/my-project");
    expect(pluginStore.plugins).toEqual([enabledInfo]);
    expect(pluginStore.activations).toEqual(states);
  });

  // 11. togglePluginEnabled(false) 调用 disablePlugin 而非 enablePlugin
  it("togglePluginEnabled(false) 调用 setPluginEnabled 与 disablePlugin", async () => {
    const disabledInfo = { ...pluginGitHelper, enabled: false };
    mocks.listPlugins.mockResolvedValue([disabledInfo]);
    mocks.listPluginStates.mockReturnValue([
      { name: "git-helper", status: "disabled" as const },
    ]);

    await togglePluginEnabled("git-helper", false);

    expect(mocks.setPluginEnabled).toHaveBeenCalledWith("git-helper", false);
    expect(mocks.disablePlugin).toHaveBeenCalledWith("git-helper");
    expect(mocks.enablePlugin).not.toHaveBeenCalled();
    expect(pluginStore.plugins[0].enabled).toBe(false);
  });

  // 12. togglePluginEnabled 出错时记录错误
  it("togglePluginEnabled 出错时记录错误", async () => {
    mocks.setPluginEnabled.mockRejectedValue(new Error("persist failed"));

    await togglePluginEnabled("git-helper", true);

    expect(pluginStore.error).toBe("persist failed");
    // 失败后不应继续调用 enablePlugin
    expect(mocks.enablePlugin).not.toHaveBeenCalled();
  });

  // 13. activatePluginByName 调用 activatePlugin 并刷新 activations
  it("activatePluginByName 调用 activatePlugin 并刷新 activations", async () => {
    const states = [{ name: "git-helper", status: "activated" as const }];
    mocks.listPluginStates.mockReturnValue(states);

    await activatePluginByName("git-helper");

    expect(mocks.activatePlugin).toHaveBeenCalledWith("git-helper");
    expect(pluginStore.activations).toEqual(states);
  });

  // 14. deactivatePluginByName 调用 deactivatePlugin 并刷新 activations
  it("deactivatePluginByName 调用 deactivatePlugin 并刷新 activations", async () => {
    const states = [{ name: "git-helper", status: "loaded" as const }];
    mocks.listPluginStates.mockReturnValue(states);

    await deactivatePluginByName("git-helper");

    expect(mocks.deactivatePlugin).toHaveBeenCalledWith("git-helper");
    expect(pluginStore.activations).toEqual(states);
  });

  // 15. retryPluginActivation 先 deactivate 再 activate，最后刷新状态
  it("retryPluginActivation 先停用再启用并刷新状态", async () => {
    const states = [{ name: "git-helper", status: "activated" as const }];
    mocks.listPluginStates.mockReturnValue(states);

    await retryPluginActivation("git-helper");

    expect(mocks.deactivatePlugin).toHaveBeenCalledWith("git-helper");
    expect(mocks.activatePlugin).toHaveBeenCalledWith("git-helper");
    // 顺序：先 deactivate 再 activate
    expect(mocks.deactivatePlugin.mock.invocationCallOrder[0])
      .toBeLessThan(mocks.activatePlugin.mock.invocationCallOrder[0]);
    expect(pluginStore.activations).toEqual(states);
  });

  // M-30: deactivate 抛错时,先 unregisterPluginContributions 强制清理,再 activate
  it("M-30: deactivate 抛错时调用 unregisterPluginContributions 后继续 activate", async () => {
    // 让 deactivatePlugin 抛错,模拟插件 deactivate 失败的场景
    mocks.deactivatePlugin.mockRejectedValueOnce(new Error("deactivate boom"));
    const states = [{ name: "git-helper", status: "activated" as const }];
    mocks.listPluginStates.mockReturnValue(states);

    await retryPluginActivation("git-helper");

    // deactivate 被调用
    expect(mocks.deactivatePlugin).toHaveBeenCalledWith("git-helper");
    // 抛错后强制清理 contributions
    expect(mocks.unregisterPluginContributions).toHaveBeenCalledWith("git-helper");
    // 仍然继续 activate（未被 deactivate 错误阻断）
    expect(mocks.activatePlugin).toHaveBeenCalledWith("git-helper");
    // 顺序：deactivate → unregisterPluginContributions → activate
    const deactivateOrder = mocks.deactivatePlugin.mock.invocationCallOrder[0];
    const unregisterOrder = mocks.unregisterPluginContributions.mock.invocationCallOrder[0];
    const activateOrder = mocks.activatePlugin.mock.invocationCallOrder[0];
    expect(unregisterOrder).toBeGreaterThan(deactivateOrder);
    expect(activateOrder).toBeGreaterThan(unregisterOrder);
    // 最终刷新 activations
    expect(pluginStore.activations).toEqual(states);
  });

  // M-30: deactivate 成功时不调用 unregisterPluginContributions（deactivatePlugin 内部已清理）
  it("M-30: deactivate 成功时不额外调用 unregisterPluginContributions", async () => {
    mocks.listPluginStates.mockReturnValue([]);

    await retryPluginActivation("git-helper");

    expect(mocks.deactivatePlugin).toHaveBeenCalledWith("git-helper");
    // deactivate 成功 → 不需要额外清理
    expect(mocks.unregisterPluginContributions).not.toHaveBeenCalled();
    expect(mocks.activatePlugin).toHaveBeenCalledWith("git-helper");
  });

  // 16. reloadPlugins 等价于 loadPlugins 后再 activateStartupPlugins
  it("reloadPlugins 调用 loadPlugins 与 activateStartupPlugins", async () => {
    mocks.listPlugins.mockResolvedValue([pluginGitHelper]);
    mocks.listPluginStates.mockReturnValue([
      { name: "git-helper", status: "activated" as const },
    ]);

    await reloadPlugins();

    // loadPlugins 路径：listPlugins + syncPlugins
    expect(mocks.listPlugins).toHaveBeenCalledWith("/home/user/my-project");
    expect(mocks.syncPlugins).toHaveBeenCalledWith([pluginGitHelper]);
    // activateStartupPlugins 路径
    expect(mocks.activateOnStartup).toHaveBeenCalledTimes(1);
    expect(pluginStore.plugins).toEqual([pluginGitHelper]);
  });
});
