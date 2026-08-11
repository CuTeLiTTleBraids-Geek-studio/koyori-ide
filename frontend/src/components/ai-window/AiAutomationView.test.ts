import { mount } from "@vue/test-utils";
import { describe, expect, it, vi } from "vitest";
import AiAutomationView from "./AiAutomationView.vue";

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
}));

const wrapperFactory = () => mount(AiAutomationView, {
  global: {
    stubs: {
      PlanSection: { template: '<div data-section="plans" />' },
      GoalSection: { template: '<div data-section="goals" />' },
      WorkflowSection: { template: '<div data-section="workflows" />' },
    },
  },
});

describe("AiAutomationView", () => {
  it("switches among plans, goals, and workflows", async () => {
    const wrapper = wrapperFactory();
    expect(wrapper.find('[data-section="plans"]').exists()).toBe(true);

    await wrapper.get('[data-tab="goals"]').trigger("click");
    expect(wrapper.find('[data-section="goals"]').exists()).toBe(true);

    await wrapper.get('[data-tab="workflows"]').trigger("click");
    expect(wrapper.find('[data-section="workflows"]').exists()).toBe(true);
    expect(wrapper.findAll('[role="tab"]')).toHaveLength(3);
  });
});
