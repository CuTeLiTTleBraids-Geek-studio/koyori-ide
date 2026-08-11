import { mount } from "@vue/test-utils";
import { describe, expect, it, vi } from "vitest";
import AiWorkspaceSidebar from "./AiWorkspaceSidebar.vue";

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({
    t: (key: string) => ({
      "aiWorkspace.assistant": "AI Assistant",
      "aiWorkspace.assistantDesc": "Chat, inspect code, and apply changes.",
      "aiWorkspace.skills": "Skills",
      "aiWorkspace.skillsDesc": "Manage reusable AI instructions and tools.",
      "aiWorkspace.automation": "Automation",
      "aiWorkspace.automationDesc": "Run plans, goals, and workflows.",
      "aiWorkspace.settings": "AI Settings",
      "aiWorkspace.settingsDesc": "Configure models, tools, permissions, and integrations.",
      "aiWorkspace.rollback": "Smart Rollback",
      "aiWorkspace.rollbackDesc": "Review snapshots and restore workspace state.",
      "aiWorkspace.terminal": "Terminal",
      "aiWorkspace.resizeSidebar": "Resize AI sidebar",
    }[key] ?? key),
  }),
}));

const ConversationSidebarStub = {
  props: ["width", "embedded"],
  emits: ["select"],
  template: '<div class="conversation-stub" @click="$emit(\'select\', \'conversation-1\')" />',
};

function createWrapper(width = 288) {
  return mount(AiWorkspaceSidebar, {
    props: { activeView: "assistant", width, terminalVisible: false },
    global: {
      stubs: {
        ConversationSidebar: ConversationSidebarStub,
        "el-icon": { template: "<span><slot /></span>" },
      },
    },
  });
}

describe("AiWorkspaceSidebar", () => {
  it("shows five described workspace destinations and permanent conversations", () => {
    const wrapper = createWrapper();
    expect(wrapper.text()).toContain("AI Assistant");
    expect(wrapper.text()).toContain("Skills");
    expect(wrapper.text()).toContain("Automation");
    expect(wrapper.text()).toContain("AI Settings");
    expect(wrapper.text()).toContain("Smart Rollback");
    expect(wrapper.text()).toContain("Chat, inspect code, and apply changes.");
    expect(wrapper.text()).not.toContain("koyori-ide AI 对话");
    expect(wrapper.find(".conversation-stub").exists()).toBe(true);
  });

  it("emits navigation and conversation selection without closing", async () => {
    const wrapper = createWrapper();
    await wrapper.get('[data-view="skills"]').trigger("click");
    await wrapper.get(".conversation-stub").trigger("click");
    expect(wrapper.emitted("select-view")?.[0]).toEqual(["skills"]);
    expect(wrapper.emitted("select-conversation")?.[0]).toEqual(["conversation-1"]);
  });

  it("resizes in both directions and clamps from the keyboard", async () => {
    const wrapper = createWrapper();
    const separator = wrapper.get('[role="separator"]');

    await separator.trigger("keydown", { key: "ArrowRight" });
    await wrapper.setProps({ width: 348 });
    await separator.trigger("keydown", { key: "ArrowLeft" });
    await separator.trigger("keydown", { key: "Home" });
    await separator.trigger("keydown", { key: "End" });

    const resizeEvents = wrapper.emitted("resize") ?? [];
    expect(resizeEvents.some(([value]) => Number(value) > 288)).toBe(true);
    expect(resizeEvents.some(([value]) => Number(value) < 348)).toBe(true);
    expect(resizeEvents.some(([value]) => value === 260)).toBe(true);
    expect(resizeEvents.some(([value]) => value === 380)).toBe(true);
  });
});
