import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  toggleAIWindow: vi.fn().mockResolvedValue(undefined),
  isAIWindowOpen: vi.fn().mockResolvedValue(true),
  isAIWindowVisible: vi.fn().mockResolvedValueOnce(true).mockResolvedValue(false),
  openAIWindow: vi.fn(),
  notifyError: vi.fn(),
  route: { path: "/editor" },
  routerPush: vi.fn(),
}));

vi.mock("vue-router", () => ({
  useRoute: () => mocks.route,
  useRouter: () => ({ push: mocks.routerPush }),
}));

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
  translate: (key: string) => key,
}));

vi.mock("@/api/services", () => ({
  windowService: {
    toggleAIWindow: mocks.toggleAIWindow,
    isAIWindowOpen: mocks.isAIWindowOpen,
    isAIWindowVisible: mocks.isAIWindowVisible,
  },
}));

vi.mock("@/stores/aiAssistant", () => ({
  openAIDesktopWindow: mocks.openAIWindow,
  toggleAIDesktopWindow: mocks.openAIWindow,
}));

vi.mock("@/lib/notifications", () => ({ notifyError: mocks.notifyError }));

vi.mock("@/lib/vscodeExtensions", () => ({
  listAllVscodeExtensionViews: () => ({
    explorer: [{ id: "test-ext.view1", name: "Test Ext View" }],
  }),
}));

import ActivityBar from "./ActivityBar.vue";
import { appState } from "@/stores/app";

describe("ActivityBar AI window state", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    appState.panelTab = "explorer";
    appState.sidebarCollapsed = false;
    mocks.toggleAIWindow.mockClear();
    mocks.isAIWindowOpen.mockClear();
    mocks.isAIWindowVisible.mockReset();
    mocks.isAIWindowVisible.mockResolvedValueOnce(true).mockResolvedValue(false);
    mocks.openAIWindow.mockReset().mockResolvedValue(undefined);
    mocks.notifyError.mockClear();
    mocks.route.path = "/editor";
    mocks.routerPush.mockClear();
    appState.activeExtensionView = null;
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("focuses an already-visible AI window through the conversation handoff entry", async () => {
    mocks.isAIWindowVisible.mockReset().mockResolvedValue(true);
    const wrapper = mount(ActivityBar, {
      global: {
        stubs: {
          "el-icon": { template: "<span><slot /></span>" },
          component: { template: "<span />" },
        },
      },
    });
    await flushPromises();

    // F-1: callHierarchy tab 已插入到 extensions 与 ai 之间，按钮索引会变，
    // 改用 aria-label 定位 AI 按钮以避免索引耦合（i18n mock 返回 key 本身）。
    const aiButton = wrapper.findAll("button").find(
      (b) => b.attributes("aria-label") === "activity.ai",
    )!;
    expect(aiButton).toBeTruthy();
    expect(aiButton.classes()).toContain("activity-bar__item--active");

    await aiButton.trigger("click");
    await flushPromises();

    expect(mocks.openAIWindow).toHaveBeenCalledTimes(1);
    expect(mocks.toggleAIWindow).not.toHaveBeenCalled();
    expect(aiButton.classes()).toContain("activity-bar__item--active");
    wrapper.unmount();
  });

  it("ignores an older visibility result that resolves after the post-open refresh", async () => {
    let resolveOlder!: (visible: boolean) => void;
    let resolveLatest!: (visible: boolean) => void;
    mocks.isAIWindowVisible.mockReset()
      .mockImplementationOnce(() => new Promise((resolve) => { resolveOlder = resolve; }))
      .mockImplementationOnce(() => new Promise((resolve) => { resolveLatest = resolve; }));
    const wrapper = mount(ActivityBar, {
      global: {
        stubs: {
          "el-icon": { template: "<span><slot /></span>" },
          component: { template: "<span />" },
        },
      },
    });
    const aiButton = wrapper.findAll("button").find(
      (button) => button.attributes("aria-label") === "activity.ai",
    )!;

    await aiButton.trigger("click");
    await flushPromises();
    resolveLatest(true);
    await flushPromises();
    expect(aiButton.classes()).toContain("activity-bar__item--active");

    resolveOlder(false);
    await flushPromises();
    expect(aiButton.classes()).toContain("activity-bar__item--active");
    wrapper.unmount();
  });

  it("clears the AI window polling interval on unmount", async () => {
    const clearIntervalSpy = vi.spyOn(globalThis, "clearInterval");
    const wrapper = mount(ActivityBar, {
      global: {
        stubs: {
          "el-icon": { template: "<span><slot /></span>" },
          component: { template: "<span />" },
        },
      },
    });
    await flushPromises();

    expect(vi.getTimerCount()).toBeGreaterThan(0);
    wrapper.unmount();

    expect(clearIntervalSpy).toHaveBeenCalledOnce();
    expect(vi.getTimerCount()).toBe(0);
    clearIntervalSpy.mockRestore();
  });

  it("defaults to explorer, search, git, extensions, and AI plus settings", async () => {
    const wrapper = mount(ActivityBar, {
      global: {
        stubs: {
          "el-icon": { template: "<span><slot /></span>" },
          component: { template: "<span />" },
        },
      },
    });
    await flushPromises();
    const labels = wrapper.findAll("button").map((button) => button.attributes("aria-label"));
    const builtin = labels.filter((label) => label?.startsWith("activity."));
    expect(builtin).toEqual([
      "activity.explorer",
      "activity.search",
      "activity.sourceControl",
      "activity.extensions",
      "activity.ai",
      "activity.settings",
    ]);
    for (const hidden of [
      "activity.debug",
      "activity.testExplorer",
      "activity.build",
      "activity.database",
      "activity.httpClient",
      "activity.inspections",
      "activity.callHierarchy",
    ]) {
      expect(labels).not.toContain(hidden);
    }
    wrapper.unmount();
  });

  it.each([
    ["/debug", "activity.explorer", "explorer"],
    ["/test", "activity.search", "search"],
  ])("opens the %s sidebar and returns to the editor from the %s full view", async (path, label, tab) => {
    mocks.route.path = path;
    const wrapper = mount(ActivityBar, {
      global: {
        stubs: {
          "el-icon": { template: "<span><slot /></span>" },
          component: { template: "<span />" },
        },
      },
    });
    await flushPromises();

    const button = wrapper.findAll("button").find(
      (candidate) => candidate.attributes("aria-label") === label,
    )!;
    await button.trigger("click");

    expect(appState.panelTab).toBe(tab);
    expect(appState.sidebarCollapsed).toBe(false);
    expect(mocks.routerPush).toHaveBeenCalledWith("/editor");
    wrapper.unmount();
  });

  it("returns to the editor when clicking an extension view from the debug full view", async () => {
    mocks.route.path = "/debug";
    const wrapper = mount(ActivityBar, {
      global: {
        stubs: {
          "el-icon": { template: "<span><slot /></span>" },
          component: { template: "<span />" },
        },
      },
    });
    await flushPromises();

    const button = wrapper.findAll("button").find(
      (candidate) => candidate.attributes("aria-label") === "Test Ext View",
    )!;
    expect(button).toBeTruthy();
    await button.trigger("click");

    expect(appState.activeExtensionView).toBe("test-ext.view1");
    expect(appState.sidebarCollapsed).toBe(false);
    expect(mocks.routerPush).toHaveBeenCalledWith("/editor");
    wrapper.unmount();
  });

  it("uses the localized fallback when toggling the AI window fails with a non-Error value", async () => {
    mocks.openAIWindow.mockRejectedValueOnce("failed");
    const wrapper = mount(ActivityBar, {
      global: {
        stubs: {
          "el-icon": { template: "<span><slot /></span>" },
          component: { template: "<span />" },
        },
      },
    });
    await flushPromises();
    const aiButton = wrapper.findAll("button").find(
      (candidate) => candidate.attributes("aria-label") === "activity.ai",
    )!;
    await aiButton.trigger("click");
    await flushPromises();

    expect(mocks.notifyError).toHaveBeenCalledWith("aiWindow.toggleFailed");
    wrapper.unmount();
  });
});
