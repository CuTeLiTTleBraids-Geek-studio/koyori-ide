import { mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  debugState: {
    busy: false,
    lastError: "",
    running: false,
    stopped: false,
    stopReason: "",
    activeConfigName: "",
  },
  testState: {
    entries: [] as Array<{ id: string; status: string }>,
    tree: [] as Array<unknown>,
    loading: false,
    running: false,
    error: "",
    outputsByTest: {} as Record<string, string>,
    selectedTestId: "",
    continuousTesting: false,
    expanded: {} as Record<string, boolean>,
  },
  discoverTests: vi.fn().mockResolvedValue(undefined),
  loadLaunchConfigs: vi.fn().mockResolvedValue(undefined),
  refreshDebugStatus: vi.fn().mockResolvedValue(undefined),
}));

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
}));

vi.mock("@/components/layout/DebugPanel.vue", () => ({
  default: { template: '<div data-test="debug-panel" />' },
}));

vi.mock("@/stores/debug", () => ({
  debugState: mocks.debugState,
  loadLaunchConfigs: mocks.loadLaunchConfigs,
  refreshDebugStatus: mocks.refreshDebugStatus,
}));

vi.mock("@/stores/app", () => ({
  appState: { currentProject: "/workspace" },
}));

vi.mock("@/stores/testExplorer", () => ({
  testExplorerState: mocks.testState,
  discoverTests: mocks.discoverTests,
  runDiscoveredTest: vi.fn(),
  debugDiscoveredTest: vi.fn(),
  coverageDiscoveredTest: vi.fn(),
  runGoTestsJSON: vi.fn(),
  jumpToTest: vi.fn(),
  selectTest: vi.fn(),
  selectedTestOutput: vi.fn(() => ""),
  toggleSuite: vi.fn(),
  setContinuousTesting: vi.fn(),
}));

import DebugView from "./DebugView.vue";
import TestView from "./TestView.vue";

describe("Debug and Test ordinary views", () => {
  beforeEach(() => {
    Object.assign(mocks.debugState, {
      busy: false,
      lastError: "",
      running: false,
      stopped: false,
      stopReason: "",
      activeConfigName: "",
    });
    Object.assign(mocks.testState, {
      entries: [],
      tree: [],
      loading: false,
      running: false,
      error: "",
      outputsByTest: {},
      selectedTestId: "",
      continuousTesting: false,
      expanded: {},
    });
    mocks.discoverTests.mockClear();
    mocks.loadLaunchConfigs.mockClear();
    mocks.refreshDebugStatus.mockClear();
  });

  it("exposes Debug loading, error, and idle states", () => {
    mocks.debugState.busy = true;
    let wrapper = mount(DebugView);
    expect(wrapper.get(".debug-view").attributes("aria-busy")).toBe("true");
    expect(wrapper.get('[role="status"]').text()).toContain("view.debug.loading");
    wrapper.unmount();

    Object.assign(mocks.debugState, { busy: false, lastError: "adapter unavailable" });
    wrapper = mount(DebugView);
    expect(wrapper.get('[role="alert"]').text()).toContain("adapter unavailable");
    wrapper.unmount();

    mocks.debugState.lastError = "";
    wrapper = mount(DebugView);
    expect(wrapper.get('[data-state="empty"]').text()).toContain("view.debug.idle");
    wrapper.unmount();
  });

  it("keeps Test loading, error, and empty states visible", () => {
    mocks.testState.loading = true;
    let wrapper = mount(TestView);
    expect(wrapper.get(".test-view").attributes("aria-busy")).toBe("true");
    expect(wrapper.text()).toContain("view.test.loading");
    wrapper.unmount();

    Object.assign(mocks.testState, { loading: false, error: "discovery failed" });
    wrapper = mount(TestView);
    expect(wrapper.get('[role="alert"]').text()).toContain("discovery failed");
    wrapper.unmount();

    mocks.testState.error = "";
    wrapper = mount(TestView);
    expect(wrapper.get(".test-view__empty").text()).toContain("view.test.empty");
    wrapper.unmount();
  });
});
