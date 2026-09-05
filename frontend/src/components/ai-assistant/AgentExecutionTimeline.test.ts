import { mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { nextTick } from "vue";

const { timelineState } = vi.hoisted(() => ({
  timelineState: {
    entries: [] as Array<Record<string, unknown>>,
  },
}));

vi.mock("@/stores/agentTimeline", async () => {
  const { reactive } = await import("vue");
  return { agentTimelineState: reactive(timelineState) };
});

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
}));

import AgentExecutionTimeline from "./AgentExecutionTimeline.vue";
import { agentTimelineState } from "@/stores/agentTimeline";

describe("AgentExecutionTimeline", () => {
  beforeEach(() => {
    agentTimelineState.entries.splice(0);
  });

  it("renders explicit reasoning and tool lifecycle stages", () => {
    agentTimelineState.entries.push(
      {
        id: "reason-1",
        kind: "reasoning",
        stage: "reasoning",
        explicit: true,
        detail: "Explicit provider summary",
        createdAt: Date.now(),
        updatedAt: Date.now(),
      },
      {
        id: "tool-1",
        kind: "tool",
        stage: "executing",
        toolCallId: "call-1",
        tool: "read",
        target: "src/main.ts",
        detail: "src/main.ts",
        createdAt: Date.now(),
        updatedAt: Date.now(),
      },
    );

    const wrapper = mount(AgentExecutionTimeline);
    expect(wrapper.find(".agent-timeline").exists()).toBe(true);
    expect(wrapper.findAll(".agent-timeline__entry")).toHaveLength(2);
    expect(wrapper.text()).toContain("Explicit provider summary");
    expect(wrapper.text()).toContain("read");
  });

  it("does not render anything when no explicit timeline entries exist", () => {
    const wrapper = mount(AgentExecutionTimeline);
    expect(wrapper.find(".agent-timeline").exists()).toBe(false);
  });

  it("maps internal lifecycle stages to user-facing locale keys", () => {
    agentTimelineState.entries.push(
      { id: "requested", kind: "tool", stage: "requested", status: "pending", tool: "read", createdAt: 1, updatedAt: 1 },
      { id: "waiting", kind: "tool", stage: "approval", status: "waiting-approval", tool: "read", createdAt: 2, updatedAt: 2 },
      { id: "approved", kind: "tool", stage: "approval", status: "approved", tool: "read", createdAt: 3, updatedAt: 3 },
      { id: "executed", kind: "tool", stage: "result", status: "executed", tool: "read", createdAt: 4, updatedAt: 4 },
    );

    const wrapper = mount(AgentExecutionTimeline);
    const text = wrapper.text();
    expect(text).toContain("aiChat.timeline.tool-requested");
    expect(text).toContain("aiChat.timeline.waiting-approval");
    expect(text).toContain("aiChat.timeline.approved");
    expect(text).toContain("aiChat.timeline.executed");
    expect(text).not.toContain("aiChat.timeline.requested");
    expect(text).not.toContain("aiChat.timeline.approval");
  });

  it("follows newly appended activity while the user is at the bottom", async () => {
    agentTimelineState.entries.push({
      id: "initial",
      kind: "tool",
      stage: "requested",
      status: "pending",
      tool: "read",
      createdAt: 1,
      updatedAt: 1,
    });
    const wrapper = mount(AgentExecutionTimeline);
    const section = wrapper.find(".agent-timeline").element as HTMLElement;
    Object.defineProperties(section, {
      clientHeight: { configurable: true, value: 100 },
      scrollHeight: { configurable: true, writable: true, value: 200 },
      scrollTop: { configurable: true, writable: true, value: 0 },
    });
    await nextTick();
    expect(section.scrollTop).toBe(200);

    section.scrollTop = 200;
    Object.defineProperty(section, "scrollHeight", { configurable: true, writable: true, value: 280 });
    agentTimelineState.entries.push({
      id: "result",
      kind: "tool",
      stage: "result",
      status: "executed",
      tool: "read",
      createdAt: 2,
      updatedAt: 2,
    });
    await nextTick();
    await nextTick();
    expect(section.scrollTop).toBe(280);
    wrapper.unmount();
  });

  it("preserves a user's history position while new activity arrives", async () => {
    agentTimelineState.entries.push({
      id: "initial",
      kind: "reasoning",
      stage: "reasoning",
      detail: "summary",
      createdAt: 1,
      updatedAt: 1,
    });
    const wrapper = mount(AgentExecutionTimeline);
    const section = wrapper.find(".agent-timeline").element as HTMLElement;
    Object.defineProperties(section, {
      clientHeight: { configurable: true, value: 100 },
      scrollHeight: { configurable: true, writable: true, value: 400 },
      scrollTop: { configurable: true, writable: true, value: 0 },
    });
    await nextTick();
    section.scrollTop = 40;
    section.dispatchEvent(new Event("scroll"));
    Object.defineProperty(section, "scrollHeight", { configurable: true, writable: true, value: 520 });
    agentTimelineState.entries.push({
      id: "tool",
      kind: "tool",
      stage: "executing",
      status: "executing",
      tool: "search",
      createdAt: 2,
      updatedAt: 2,
    });
    await nextTick();
    await nextTick();
    expect(section.scrollTop).toBe(40);
    wrapper.unmount();
  });

  it("follows in-place lifecycle updates, not only appended entries", async () => {
    agentTimelineState.entries.push({
      id: "tool",
      kind: "tool",
      stage: "approval",
      status: "waiting-approval",
      tool: "read",
      createdAt: 1,
      updatedAt: 1,
    });
    const wrapper = mount(AgentExecutionTimeline);
    const section = wrapper.find(".agent-timeline").element as HTMLElement;
    Object.defineProperties(section, {
      clientHeight: { configurable: true, value: 100 },
      scrollHeight: { configurable: true, writable: true, value: 210 },
      scrollTop: { configurable: true, writable: true, value: 210 },
    });
    await nextTick();
    agentTimelineState.entries[0].stage = "result";
    agentTimelineState.entries[0].status = "executed";
    agentTimelineState.entries[0].updatedAt = 2;
    Object.defineProperty(section, "scrollHeight", { configurable: true, writable: true, value: 230 });
    await nextTick();
    await nextTick();
    expect(section.scrollTop).toBe(230);
    expect(wrapper.text()).toContain("aiChat.timeline.executed");
    wrapper.unmount();
  });

  it("resets follow mode after a completed timeline is cleared", async () => {
    agentTimelineState.entries.push({
      id: "old",
      kind: "tool",
      stage: "result",
      status: "executed",
      tool: "read",
      createdAt: 1,
      updatedAt: 1,
    });
    const wrapper = mount(AgentExecutionTimeline);
    const section = wrapper.find(".agent-timeline").element as HTMLElement;
    Object.defineProperties(section, {
      clientHeight: { configurable: true, value: 100 },
      scrollHeight: { configurable: true, writable: true, value: 600 },
      scrollTop: { configurable: true, writable: true, value: 0 },
    });
    await nextTick();
    section.scrollTop = 20;
    section.dispatchEvent(new Event("scroll"));
    agentTimelineState.entries.splice(0);
    await nextTick();
    agentTimelineState.entries.push({
      id: "new",
      kind: "reasoning",
      stage: "reasoning",
      detail: "new run",
      createdAt: 2,
      updatedAt: 2,
    });
    await nextTick();
    const newSection = wrapper.find(".agent-timeline").element as HTMLElement;
    Object.defineProperties(newSection, {
      clientHeight: { configurable: true, value: 100 },
      scrollHeight: { configurable: true, writable: true, value: 700 },
      scrollTop: { configurable: true, writable: true, value: 0 },
    });
    agentTimelineState.entries[0].detail = "new run updated";
    agentTimelineState.entries[0].updatedAt = 3;
    await nextTick();
    await nextTick();
    expect(newSection.scrollTop).toBe(700);
    wrapper.unmount();
  });
});
