import { mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";
import AiSettingsView from "./AiSettingsView.vue";

const { consumePendingTargetMock, syncHandlers } = vi.hoisted(() => ({
  consumePendingTargetMock: vi.fn(),
  syncHandlers: {} as Record<string, ((event: unknown) => void) | undefined>,
}));

vi.mock("@/stores/aiAssistant", () => ({
  consumePendingAISettingsSection: consumePendingTargetMock,
  parseAISettingsSectionEvent: (event: unknown) =>
    (event as { data?: { section?: string } })?.data?.section,
}));

vi.mock("@/lib/crossWindowSync", () => ({
  subscribeCrossWindowEvent: (name: string, handler: (event: unknown) => void) => {
    syncHandlers[name] = handler;
    return () => { delete syncHandlers[name]; };
  },
}));

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
}));

vi.mock("@/stores/app", () => ({
  appState: {
    openAIWindowOnStartup: false,
    aiWindowTheme: "apple-dark",
    aiSidebarWidth: 288,
    aiTerminalWidth: 440,
  },
  saveSettings: vi.fn(),
}));

vi.mock("@/api/services", () => ({
  windowService: {
    isAIAlwaysOnTop: vi.fn().mockResolvedValue(true),
    setAIAlwaysOnTop: vi.fn().mockResolvedValue(undefined),
  },
}));

const sectionNames = [
  "AiSection", "AgentSection", "PersonaSection", "ModelPermissionSection",
  "McpSection", "SkillsSection", "PromptsSection", "PresetsSection",
  "ComputerUseSection", "DiffSection", "RollbackSection", "ImSection",
  "PersonalizationSection", "AiWindowThemePicker",
];

const stubs = Object.fromEntries(sectionNames.map((name) => [name, {
  template: `<div data-stub="${name}" />`,
}]));

describe("AiSettingsView", () => {
  beforeEach(() => {
    consumePendingTargetMock.mockReset().mockReturnValue(undefined);
    for (const key of Object.keys(syncHandlers)) delete syncHandlers[key];
  });

  it("groups models, context, execution, integrations, and window settings", async () => {
    const wrapper = mount(AiSettingsView, { global: { stubs } });
    expect(wrapper.find('[data-stub="AiSection"]').exists()).toBe(true);

    await wrapper.get('[data-group="context"]').trigger("click");
    expect(wrapper.find('[data-stub="McpSection"]').exists()).toBe(true);
    expect(wrapper.find('[data-stub="SkillsSection"]').exists()).toBe(true);

    await wrapper.get('[data-group="execution"]').trigger("click");
    expect(wrapper.find('[data-stub="ComputerUseSection"]').exists()).toBe(true);

    await wrapper.get('[data-group="integrations"]').trigger("click");
    expect(wrapper.find('[data-stub="ImSection"]').exists()).toBe(true);

    await wrapper.get('[data-group="window"]').trigger("click");
    expect(wrapper.find('[data-stub="AiWindowThemePicker"]').exists()).toBe(true);
  });

  it("opens the group and item requested by a pending legacy deep-link", async () => {
    consumePendingTargetMock.mockReturnValue("prompts");
    const wrapper = mount(AiSettingsView, { global: { stubs } });
    await wrapper.vm.$nextTick();

    expect(wrapper.get('[data-group="context"]').classes()).toContain("is-active");
    expect(wrapper.find('[data-ai-settings-section="prompts"]').exists()).toBe(true);
  });

  it("moves an already-open AI window to a requested Computer Use section", async () => {
    const wrapper = mount(AiSettingsView, { global: { stubs } });
    syncHandlers["ai:open-settings"]?.({ data: { section: "computerUse" } });
    await wrapper.vm.$nextTick();

    expect(wrapper.get('[data-group="execution"]').classes()).toContain("is-active");
    expect(wrapper.find('[data-ai-settings-section="computerUse"]').exists()).toBe(true);
  });
});
