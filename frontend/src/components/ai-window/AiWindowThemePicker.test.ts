import { mount } from "@vue/test-utils";
import { describe, expect, it, vi } from "vitest";
import AiWindowThemePicker from "./AiWindowThemePicker.vue";

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
}));

describe("AiWindowThemePicker", () => {
  it("offers exactly five independent AI window themes", async () => {
    const wrapper = mount(AiWindowThemePicker, { props: { theme: "apple-dark" } });
    const radios = wrapper.findAll('[role="radio"]');
    expect(radios).toHaveLength(5);

    await wrapper.get('[data-theme="claude-light"]').trigger("click");
    await wrapper.get('[data-theme="system"]').trigger("click");
    expect(wrapper.emitted("update:theme")?.[0]).toEqual(["claude-light"]);
    expect(wrapper.emitted("update:theme")?.[1]).toEqual(["system"]);
  });
});
