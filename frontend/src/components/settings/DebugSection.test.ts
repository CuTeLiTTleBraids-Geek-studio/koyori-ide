import { describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import ElementPlus from "element-plus";

const { appState, debugState, loadLaunchConfigs, upsertLaunchConfig } = vi.hoisted(() => ({
  appState: { currentProject: "C:\\repo" },
  debugState: {
    activeConfigName: "Launch app",
    launchConfigs: [
      { name: "Launch app", kind: "go", request: "launch", stopOnEntry: false },
    ],
  },
  loadLaunchConfigs: vi.fn(),
  upsertLaunchConfig: vi.fn(),
}));

vi.mock("@/stores/app", () => ({ appState }));
vi.mock("@/stores/debug", () => ({ debugState, loadLaunchConfigs, upsertLaunchConfig }));
vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
}));

import DebugSection from "./DebugSection.vue";

describe("DebugSection", () => {
  it("binds launch configuration and persists stop-on-entry", async () => {
    const wrapper = mount(DebugSection, { global: { plugins: [ElementPlus] } });
    await flushPromises();

    expect(loadLaunchConfigs).toHaveBeenCalledWith("C:\\repo");
    expect(wrapper.findAll(".setting-row")).toHaveLength(2);

    const toggle = wrapper.findComponent({ name: "ElSwitch" });
    toggle.vm.$emit("update:modelValue", true);
    await flushPromises();
    expect(upsertLaunchConfig).toHaveBeenCalledWith(expect.objectContaining({
      name: "Launch app",
      stopOnEntry: true,
    }));
  });
});
