import { mount } from "@vue/test-utils";
import { nextTick } from "vue";
import { describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  state: {
    cpuProfiling: false,
    loading: false,
    analysis: {
      totalSamples: 2,
      totalDuration: 100,
      topFunctions: [],
      sampleUnit: "nanoseconds",
      sampleType: "cpu",
      flameGraph: { id: "0", name: "all", value: 100, children: [] },
    },
    lastProfilePath: "/proj/cpu.prof",
    lastError: "",
    lastKind: "cpu",
    cpuOutputPath: "",
    activeProfile: "",
    activeOutputPath: "",
  },
  startBlock: vi.fn(),
  stopBlock: vi.fn(),
  startMutex: vi.fn(),
  stopMutex: vi.fn(),
  startTrace: vi.fn(),
  stopTrace: vi.fn(),
}));

vi.mock("@/stores/pprof", () => ({
  pprofState: mocks.state,
  refreshProfilingStatus: vi.fn(),
  startCPUProfile: vi.fn(),
  stopCPUProfile: vi.fn(),
  captureHeapProfile: vi.fn(),
  captureGoroutineProfile: vi.fn(),
  startBlockProfile: mocks.startBlock,
  stopBlockProfile: mocks.stopBlock,
  startMutexProfile: mocks.startMutex,
  stopMutexProfile: mocks.stopMutex,
  startTrace: mocks.startTrace,
  stopTrace: mocks.stopTrace,
  analyzeTrace: vi.fn(),
  analyzeProfile: vi.fn(),
  clearAnalysis: vi.fn(),
  formatDuration: (value: number) => String(value),
  formatBytes: (value: number) => String(value),
}));

import ProfilePanel from "./ProfilePanel.vue";

describe("ProfilePanel advanced profiling", () => {
  it("renders the flame graph and toggles block, mutex, and trace sessions", async () => {
    const wrapper = mount(ProfilePanel, { global: { stubs: { "el-icon": true } } });
    expect(wrapper.find('[data-test="flame-graph"]').exists()).toBe(true);
    for (const control of wrapper.findAll(".profile-panel__input, .profile-panel__select")) {
      const id = control.attributes("id");
      expect(id).toBeTruthy();
      expect(wrapper.find(`label[for="${id}"]`).exists()).toBe(true);
    }

    await wrapper.get('[data-test="profile-block"]').trigger("click");
    await wrapper.get('[data-test="profile-mutex"]').trigger("click");
    await wrapper.get('[data-test="profile-trace"]').trigger("click");
    expect(mocks.startBlock).toHaveBeenCalledOnce();
    expect(mocks.startMutex).toHaveBeenCalledOnce();
    expect(mocks.startTrace).toHaveBeenCalledOnce();

    mocks.state.activeProfile = "block";
    await nextTick();
    await wrapper.get('[data-test="profile-block"]').trigger("click");
    expect(mocks.stopBlock).toHaveBeenCalledWith(true);
  });
});
