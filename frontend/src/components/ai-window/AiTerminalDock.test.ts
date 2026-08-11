import { mount } from "@vue/test-utils";
import { describe, expect, it, vi } from "vitest";
import AiTerminalDock from "./AiTerminalDock.vue";

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
}));

vi.mock("@/components/layout/TerminalPanel.vue", () => ({
  default: { template: '<div class="terminal-panel-stub" />' },
}));

function mountDock(width = 440, visible = true) {
  return mount(AiTerminalDock, {
    props: { visible, width, maxWidth: 660 },
    global: {
      stubs: {
        TerminalPanel: { template: '<div class="terminal-panel-stub" />' },
        "el-icon": { template: "<span><slot /></span>" },
      },
    },
  });
}

describe("AiTerminalDock", () => {
  it("renders only when visible", () => {
    expect(mountDock(440, false).find(".terminal-panel-stub").exists()).toBe(false);
    expect(mountDock().find(".terminal-panel-stub").exists()).toBe(true);
  });

  it("resizes from its left edge and clamps with keyboard controls", async () => {
    const wrapper = mountDock();
    const separator = wrapper.get('[role="separator"]');
    await separator.trigger("keydown", { key: "ArrowRight" });
    await separator.trigger("keydown", { key: "ArrowLeft" });
    await separator.trigger("keydown", { key: "Home" });
    await separator.trigger("keydown", { key: "End" });

    const values = (wrapper.emitted("resize") ?? []).map(([value]) => Number(value));
    expect(values.some((value) => value > 440)).toBe(true);
    expect(values.some((value) => value < 440)).toBe(true);
    expect(values).toContain(340);
    expect(values).toContain(660);
  });

  it("emits close without owning workspace navigation", async () => {
    const wrapper = mountDock();
    await wrapper.get('[data-action="close-terminal"]').trigger("click");
    expect(wrapper.emitted("close")).toHaveLength(1);
  });
});
