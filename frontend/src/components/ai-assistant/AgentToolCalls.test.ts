import { describe, expect, it, beforeEach, vi } from "vitest";
import { mount } from "@vue/test-utils";

const {
  agentStateObj,
  aiStateObj,
  approveAndFeedMock,
  rejectAndFeedMock,
  notifyWarningMock,
  clearPendingToolCallsMock,
} = vi.hoisted(() => ({
  agentStateObj: {
    mode: "agent" as string,
    pendingToolCalls: [] as Array<Record<string, unknown>>,
  },
  aiStateObj: { streaming: false, globalStreamBusy: false },
  approveAndFeedMock: vi.fn(),
  rejectAndFeedMock: vi.fn(),
  notifyWarningMock: vi.fn(),
  clearPendingToolCallsMock: vi.fn(() => { agentStateObj.pendingToolCalls = []; }),
}));

vi.mock("@/stores/ai", async () => {
  const { reactive } = await import("vue");
  return { aiState: reactive(aiStateObj) };
});

vi.mock("@/stores/agent", async () => {
  const { computed, reactive } = await import("vue");
  const state = reactive(agentStateObj);
  return {
    agentState: state,
    isAgentMode: computed(() => state.mode === "agent"),
    approveAndFeed: approveAndFeedMock,
    rejectAndFeed: rejectAndFeedMock,
    clearPendingToolCalls: clearPendingToolCallsMock,
    toggleWriteHunk: vi.fn(),
    getRegisteredTools: () => [{
      kind: "read",
      schema: { dangerLevel: "safe" },
    }, {
      kind: "write",
      schema: { dangerLevel: "elevated" },
    }],
  };
});

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
}));

vi.mock("@/lib/notifications", () => ({
  notifyWarning: notifyWarningMock,
}));

import AgentToolCalls from "./AgentToolCalls.vue";
import { agentState } from "@/stores/agent";

describe("AgentToolCalls", () => {
  beforeEach(() => {
    agentState.mode = "agent";
    agentState.pendingToolCalls = [];
    aiStateObj.streaming = false;
    aiStateObj.globalStreamBusy = false;
    approveAndFeedMock.mockReset();
    rejectAndFeedMock.mockReset();
    notifyWarningMock.mockReset();
    clearPendingToolCallsMock.mockClear();
  });

  it("shows pending tools with approval controls in the standalone agent view", async () => {
    agentState.pendingToolCalls.push({
      id: "read-1",
      kind: "read",
      target: "src/main.ts",
      status: "pending",
      arguments: { path: "src/main.ts" },
    });

    const wrapper = mount(AgentToolCalls);
    expect(wrapper.find(".agent-tool-calls").exists()).toBe(true);
    expect(wrapper.get(".agent-tool-calls").attributes("data-agent-tool-calls")).toBe("");
    expect(wrapper.find(".agent-tool-call__target").text()).toContain("src/main.ts");
    expect(wrapper.findAll(".agent-tool-call__button")).toHaveLength(2);
    const card = wrapper.get(".agent-tool-call");
    expect(card.attributes("data-agent-tool-call-id")).toBe("read-1");
    expect(card.attributes("data-agent-tool-kind")).toBe("read");
    expect(card.attributes("data-agent-tool-status")).toBe("pending");

    const approve = wrapper.get("[data-agent-tool-action='approve']");
    const reject = wrapper.get("[data-agent-tool-action='reject']");
    expect(approve.attributes("data-agent-tool-call-id")).toBe("read-1");
    expect(approve.attributes("data-agent-tool-kind")).toBe("read");

    (approve.element as HTMLButtonElement).click();
    (reject.element as HTMLButtonElement).click();
    await Promise.resolve();
    expect(approveAndFeedMock).toHaveBeenCalledWith(agentState.pendingToolCalls[0]);
    expect(rejectAndFeedMock).toHaveBeenCalledWith(agentState.pendingToolCalls[0]);
  });

  it("keeps approval controls disabled while the assistant stream is active", async () => {
    agentState.pendingToolCalls.push({ id: "run-1", kind: "run", target: "go test ./...", status: "pending" });
    aiStateObj.streaming = true;

    const wrapper = mount(AgentToolCalls);
    expect(wrapper.find(".agent-tool-calls__warning").exists()).toBe(true);
    expect(wrapper.find(".agent-tool-call__button--approve").attributes("disabled")).toBeDefined();
    await wrapper.find(".agent-tool-call__button--approve").trigger("click");
    expect(approveAndFeedMock).not.toHaveBeenCalled();
  });

  it("keeps approve, reject, and clear fail-closed until backend stream ownership is released", async () => {
    agentState.pendingToolCalls.push({
      id: "read-busy",
      kind: "read",
      target: "src/main.ts",
      status: "pending",
    });
    aiStateObj.globalStreamBusy = true;

    const wrapper = mount(AgentToolCalls);
    const approve = wrapper.get(".agent-tool-call__button--approve");
    const reject = wrapper.get(".agent-tool-call__button--reject");
    const clear = wrapper.get(".agent-tool-calls__clear");
    expect(approve.attributes("disabled")).toBeDefined();
    expect(reject.attributes("disabled")).toBeDefined();
    expect(clear.attributes("disabled")).toBeDefined();

    (approve.element as HTMLButtonElement).disabled = false;
    (reject.element as HTMLButtonElement).disabled = false;
    (clear.element as HTMLButtonElement).disabled = false;
    await approve.trigger("click");
    await reject.trigger("click");
    await clear.trigger("click");
    expect(approveAndFeedMock).not.toHaveBeenCalled();
    expect(rejectAndFeedMock).not.toHaveBeenCalled();
    expect(clearPendingToolCallsMock).not.toHaveBeenCalled();
  });

  it("does not render outside agent mode and can clear the queue", async () => {
    agentState.pendingToolCalls.push({ id: "read-2", kind: "read", target: "a.ts", status: "pending" });
    agentState.mode = "chat";
    const hidden = mount(AgentToolCalls);
    expect(hidden.find(".agent-tool-calls").exists()).toBe(false);
    hidden.unmount();

    agentState.mode = "agent";
    const wrapper = mount(AgentToolCalls);
    await wrapper.find(".agent-tool-calls__clear").trigger("click");
    expect(clearPendingToolCallsMock).toHaveBeenCalledOnce();
  });

  it("renders write hunks and accept/reject all controls", async () => {
    agentState.pendingToolCalls.push({
      id: "write-1",
      kind: "write",
      target: "note.txt",
      status: "pending",
      arguments: { path: "note.txt", content: "hello\nworld" },
      writeDiff: {
        path: "note.txt",
        oldContent: "hello",
        newContent: "hello\nworld",
        hunks: [
          { oldStart: 1, oldCount: 1, newStart: 1, newCount: 2, lines: [
            { type: "context", content: "hello" },
            { type: "added", content: "world" },
          ] },
        ],
        addedLines: 1,
        removedLines: 0,
      },
      selectedHunks: [0],
    });
    const wrapper = mount(AgentToolCalls);
    expect(wrapper.find("[data-agent-write-diff]").exists()).toBe(true);
    expect(wrapper.find(".agent-tool-call__hunk").text()).toContain("+world");
    expect(wrapper.find("[data-agent-tool-action='apply-selected']").exists()).toBe(true);
    await wrapper.find("[data-agent-tool-action='approve']").trigger("click");
    expect(approveAndFeedMock).toHaveBeenCalled();
    await wrapper.find("[data-agent-tool-action='reject']").trigger("click");
    expect(rejectAndFeedMock).toHaveBeenCalled();
  });
});
