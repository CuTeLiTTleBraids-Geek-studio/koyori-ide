import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";

let routeQuery: Record<string, string> = {};
const { openAIDesktopWindowMock, routerReplaceMock } = vi.hoisted(() => ({
  openAIDesktopWindowMock: vi.fn().mockResolvedValue(undefined),
  routerReplaceMock: vi.fn().mockResolvedValue(undefined),
}));

vi.mock("vue-router", () => ({
  useRoute: () => ({ query: routeQuery }),
  useRouter: () => ({ replace: routerReplaceMock }),
}));

vi.mock("@/stores/aiAssistant", () => ({
  openAIDesktopWindow: openAIDesktopWindowMock,
}));

vi.mock("@/lib/i18n", () => {
  const translate = (key: string) => key;
  return {
    translate,
    useI18n: () => ({ t: translate }),
  };
});

vi.mock("@/components/settings/GeneralSection.vue", () => ({ default: { name: "GeneralSection", template: "<div data-section='GeneralSection' />" } }));
vi.mock("@/components/settings/EditorSection.vue", () => ({ default: { name: "EditorSection", template: "<div data-section='EditorSection' />" } }));
vi.mock("@/components/settings/LspSection.vue", () => ({ default: { name: "LspSection", template: "<div data-section='LspSection' />" } }));
vi.mock("@/components/settings/GitSection.vue", () => ({ default: { name: "GitSection", template: "<div data-section='GitSection' />" } }));
vi.mock("@/components/settings/DebugSection.vue", () => ({ default: { name: "DebugSection", template: "<div data-section='DebugSection' />" } }));
vi.mock("@/components/settings/TerminalSection.vue", () => ({ default: { name: "TerminalSection", template: "<div data-section='TerminalSection' />" } }));
vi.mock("@/components/settings/ShortcutsSection.vue", () => ({ default: { name: "ShortcutsSection", template: "<div data-section='ShortcutsSection' />" } }));
vi.mock("@/components/settings/AppearanceSection.vue", () => ({ default: { name: "AppearanceSection", template: "<div data-section='AppearanceSection' />" } }));
vi.mock("@/components/settings/ProfileSection.vue", () => ({ default: { name: "ProfileSection", template: "<div data-section='ProfileSection' />" } }));
vi.mock("@/components/settings/AiSection.vue", () => ({ default: { name: "AiSection", template: "<div data-section='AiSection' />" } }));
vi.mock("@/components/settings/AgentSection.vue", () => ({ default: { name: "AgentSection", template: "<div data-section='AgentSection' />" } }));
vi.mock("@/components/settings/PromptsSection.vue", () => ({ default: { name: "PromptsSection", template: "<div data-section='PromptsSection' />" } }));
vi.mock("@/components/settings/PresetsSection.vue", () => ({ default: { name: "PresetsSection", template: "<div data-section='PresetsSection' />" } }));
vi.mock("@/components/settings/ai/ComputerUseSection.vue", () => ({ default: { name: "ComputerUseSection", template: "<div data-section='ComputerUseSection' />" } }));

import SettingsView from "./SettingsView.vue";

describe("SettingsView", () => {
  beforeEach(() => {
    routeQuery = {};
    openAIDesktopWindowMock.mockReset().mockResolvedValue(undefined);
    routerReplaceMock.mockReset().mockResolvedValue(undefined);
    vi.useFakeTimers();
  });
  afterEach(() => vi.useRealTimers());

  it("exposes Editor, LSP, Git, Debug, Terminal, Appearance and Shortcuts groups", () => {
    const wrapper = mount(SettingsView, {
      global: { stubs: { ElIcon: { template: "<span><slot /></span>" } } },
    });
    const labels = wrapper.findAll(".settings-nav-label").map((item) => item.text());

    expect(labels).toEqual(expect.arrayContaining([
      "settings.editor",
      "settings.lsp",
      "activity.sourceControl",
      "view.debug.title",
      "settings.terminal",
      "settings.appearance",
      "settings.shortcuts",
    ]));
    expect(labels).not.toEqual(expect.arrayContaining([
      "settings.ai",
      "settings.agent",
      "settings.prompts",
      "settings.presets",
      "settings.computerUse",
    ]));
  });

  it.each(["ai", "agent", "prompts", "presets", "computerUse"])(
    "redirects the legacy %s deep-link to the single AI settings window",
    async (section) => {
      routeQuery = { section };
      const wrapper = mount(SettingsView, {
        global: { stubs: { ElIcon: { template: "<span><slot /></span>" } } },
      });
      await flushPromises();

      expect(openAIDesktopWindowMock).toHaveBeenCalledWith(section);
      expect(routerReplaceMock).toHaveBeenCalledWith({ query: { section: "general" } });
      expect(wrapper.get("[data-section='GeneralSection']").isVisible()).toBe(true);
      for (const name of ["AiSection", "AgentSection", "PromptsSection", "PresetsSection", "ComputerUseSection"]) {
        expect(wrapper.find(`[data-section='${name}']`).exists()).toBe(false);
      }
      wrapper.unmount();
    },
  );

  it("shows an AI window error and a working retry action", async () => {
    routeQuery = { section: "prompts" };
    openAIDesktopWindowMock
      .mockRejectedValueOnce(new Error("native window unavailable"))
      .mockResolvedValueOnce(undefined);
    const wrapper = mount(SettingsView, {
      global: { stubs: { ElIcon: { template: "<span><slot /></span>" } } },
    });
    await flushPromises();

    expect(wrapper.get("[role='alert']").text()).toContain("settings.aiWindowOpenFailed");
    await wrapper.get("[data-test='retry-ai-settings']").trigger("click");
    await flushPromises();
    expect(openAIDesktopWindowMock).toHaveBeenLastCalledWith("prompts");
    expect(wrapper.find("[role='alert']").exists()).toBe(false);
    expect(routerReplaceMock).toHaveBeenCalledWith({ query: { section: "general" } });
  });

  it("debounces search for 500ms and navigates to the matched group", async () => {
    const wrapper = mount(SettingsView, {
      global: { stubs: { ElIcon: { template: "<span><slot /></span>" } } },
    });
    await wrapper.get(".settings-search__input").setValue("inlay");
    await vi.advanceTimersByTimeAsync(499);
    expect(wrapper.find(".settings-search-results").exists()).toBe(false);

    await vi.advanceTimersByTimeAsync(1);
    expect(wrapper.find(".settings-search-results").exists()).toBe(true);
    expect(wrapper.text()).toContain("editorSection.inlayHints");

    await wrapper.get(".settings-search-result").trigger("click");
    expect(wrapper.find(".settings-search-results").exists()).toBe(false);
    expect(wrapper.get("[data-section='LspSection']").isVisible()).toBe(true);
  });

  it("opens the accurate AI settings target from a migrated search result", async () => {
    const wrapper = mount(SettingsView, {
      global: { stubs: { ElIcon: { template: "<span><slot /></span>" } } },
    });
    await wrapper.get(".settings-search__input").setValue("settings.prompts");
    await vi.advanceTimersByTimeAsync(500);
    expect(wrapper.text()).toContain("settings.prompts");

    await wrapper.get(".settings-search-result").trigger("click");
    await flushPromises();
    expect(openAIDesktopWindowMock).toHaveBeenCalledWith("prompts");
    expect(routerReplaceMock).toHaveBeenCalledWith({ query: { section: "general" } });
  });
});
