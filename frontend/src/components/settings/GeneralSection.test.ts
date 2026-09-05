import { flushPromises, shallowMount, type VueWrapper } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import GeneralSection from "./GeneralSection.vue";
import { logLevelService } from "@/api/services";

vi.mock("@/lib/i18n", () => {
  const translate = (key: string) => key;
  return {
    translate,
    useI18n: () => ({ t: translate }),
  };
});

vi.mock("@/lib/pluginRegistry", () => ({
  isProductionSandboxRequired: () => true,
}));

describe("GeneralSection", () => {
  let wrapper: VueWrapper | undefined;

  function mountSection() {
    wrapper = shallowMount(GeneralSection, {
      global: {
        stubs: {
          // Do not execute table scoped slots without Element Plus row props.
          ElTable: { template: "<div />" },
          ElTableColumn: { template: "<div />" },
        },
      },
    });
    return wrapper;
  }

  beforeEach(() => {
    vi.spyOn(logLevelService, "getLogPath").mockResolvedValue("");
  });

  afterEach(async () => {
    await flushPromises();
    wrapper?.unmount();
    wrapper = undefined;
    vi.restoreAllMocks();
  });

  it("does not render AI-window startup settings", () => {
    const wrapper = mountSection();
    expect(wrapper.text()).not.toContain("general.openAIWindowOnStartup");
  });

  it("describes verified download and manual installation without one-click update", () => {
    const wrapper = mountSection();
    expect(wrapper.text()).toContain("mainLayout.commandCheckUpdates");
    expect(wrapper.text()).toContain("general.autoUpdateUnavailable");
    expect(wrapper.text()).not.toMatch(/install now|一键更新|今すぐインストール/i);
  });

  it("disables the plugin sandbox switch when production forces sandbox (P13-G02 / UI-3)", () => {
    const wrapper = mountSection();
    const sandboxSwitch = wrapper.get("el-switch");
    expect(sandboxSwitch.attributes("disabled")).toBeDefined();
    expect(wrapper.text()).toContain("general.pluginSandboxForcedHint");
    expect(wrapper.text()).not.toContain("general.pluginSandboxHint");
  });
});
