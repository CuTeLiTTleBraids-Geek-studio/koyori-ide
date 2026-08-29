import { describe, expect, it, vi } from "vitest";
import {
  clickManualAgentToolDecision,
  waitForAgentToolRoundCompletion as waitForAgentToolRoundCompletionCore,
  type AgentToolRoundSnapshot,
} from "./agentToolRoundProbeCore";

const readRoundContract = {
  toolKind: "read" as const,
  expectedUsageOperation: "read" as const,
  approvalMode: "auto-approve" as const,
  expectedDecision: "approve" as const,
  expectedOutcome: "executed" as const,
};

function waitForAgentToolRoundCompletion(
  options: Omit<
    Parameters<typeof waitForAgentToolRoundCompletionCore>[0],
    keyof typeof readRoundContract
  >,
) {
  return waitForAgentToolRoundCompletionCore({
    ...readRoundContract,
    ...options,
  });
}

function fakeClock() {
  let current = 0;
  return {
    now: () => current,
    sleep: async (milliseconds: number) => {
      current += milliseconds;
    },
  };
}

const completedSnapshot: AgentToolRoundSnapshot = {
  streaming: false,
  globalStreamBusy: false,
  error: null,
  executionUsage: {
    unitId: "unit-packaged-agent-read",
    sessionId: "chat-packaged-agent",
    operation: "read",
    success: true,
    pending: false,
    sessionMatchesRequest: true,
    observationMatchesToolResult: true,
  },
  messages: [
    { role: "user", content: "Read the packaged fixture." },
    {
      role: "assistant",
      content: "",
      toolCalls: [{
        id: "call_packaged_agent_read",
        name: "read",
        arguments: '{"path":"agent-tool-round.txt"}',
      }],
    },
    {
      role: "tool",
      content: "Read agent-tool-round.txt:\nPACKAGED_AGENT_TOOL_OBSERVATION",
      toolResults: [{
        toolCallId: "call_packaged_agent_read",
        content: "Read agent-tool-round.txt:\nPACKAGED_AGENT_TOOL_OBSERVATION",
        isError: false,
      }],
    },
    { role: "assistant", content: "PACKAGED_AGENT_TOOL_ROUND_COMPLETE" },
  ],
  toolCalls: [
    {
      id: "call_packaged_agent_read",
      kind: "read",
      status: "executed",
      result: "Read agent-tool-round.txt:\nPACKAGED_AGENT_TOOL_OBSERVATION",
    },
  ],
  timeline: [
    { toolCallId: "call_packaged_agent_read", stage: "requested", status: "pending" },
    { toolCallId: "call_packaged_agent_read", stage: "approval", status: "approved" },
    { toolCallId: "call_packaged_agent_read", stage: "executing", status: "executing" },
    { toolCallId: "call_packaged_agent_read", stage: "result", status: "executed" },
    { toolCallId: "call_packaged_agent_read", stage: "observation", status: "observation" },
  ],
};

const completedRunSnapshot: AgentToolRoundSnapshot = {
  ...completedSnapshot,
  executionUsage: {
    ...completedSnapshot.executionUsage!,
    unitId: "unit-packaged-agent-run",
    operation: "run",
    externalReceiptId: "receipt-packaged-agent-run",
    externalReceiptReversible: false,
    externalCompensation: "not-needed",
  },
  messages: [
    { role: "user", content: "Run the packaged fixture." },
    {
      role: "assistant",
      content: "",
      toolCalls: [{
        id: "call_packaged_agent_run_approve",
        name: "run",
        arguments: '{"command":"fixture","cwd":"."}',
      }],
    },
    {
      role: "tool",
      content: "PACKAGED_AGENT_TOOL_OBSERVATION",
      toolResults: [{
        toolCallId: "call_packaged_agent_run_approve",
        content: "PACKAGED_AGENT_TOOL_OBSERVATION",
        isError: false,
      }],
    },
    { role: "assistant", content: "PACKAGED_AGENT_RUN_APPROVE_ROUND_COMPLETE" },
  ],
  toolCalls: [{
    id: "call_packaged_agent_run_approve",
    kind: "run",
    status: "executed",
    result: "PACKAGED_AGENT_TOOL_OBSERVATION",
  }],
  timeline: [
    { toolCallId: "call_packaged_agent_run_approve", stage: "requested", status: "pending" },
    { toolCallId: "call_packaged_agent_run_approve", stage: "approval", status: "approved" },
    { toolCallId: "call_packaged_agent_run_approve", stage: "executing", status: "executing" },
    { toolCallId: "call_packaged_agent_run_approve", stage: "result", status: "executed" },
    { toolCallId: "call_packaged_agent_run_approve", stage: "observation", status: "observation" },
  ],
};

describe("packaged Agent tool-round evidence", () => {
  it("accepts only an executed native call followed by its observation and a terminal reply", async () => {
    const clock = fakeClock();
    const snapshots: AgentToolRoundSnapshot[] = [
      {
        streaming: true,
        globalStreamBusy: true,
        error: null,
        messages: [{ role: "user", content: "Read the packaged fixture." }],
        toolCalls: [],
        timeline: [],
      },
      completedSnapshot,
    ];
    const readSnapshot = vi.fn(
      () => snapshots.shift() ?? completedSnapshot,
    );

    const result = await waitForAgentToolRoundCompletion({
      expectedToolCallId: "call_packaged_agent_read",
      expectedObservation: "PACKAGED_AGENT_TOOL_OBSERVATION",
      expectedFinalAssistant: "PACKAGED_AGENT_TOOL_ROUND_COMPLETE",
      readSnapshot,
      timeoutMs: 1_000,
      pollIntervalMs: 25,
      ...clock,
    });

    expect(result).toMatchObject({
      nativeToolCallObserved: true,
      approvalObserved: true,
      approvalPrecededExecution: true,
      backendExecutionObserved: true,
      executionUsageObserved: true,
      usageUnitId: "unit-packaged-agent-read",
      usageSessionId: "chat-packaged-agent",
      usageOperation: "read",
      usageSuccess: true,
      usagePending: false,
      usageSessionMatchesRequest: true,
      usageObservationMatchesResult: true,
      observationSubmitted: true,
      nativeProtocolResultSubmitted: true,
      finalAssistantObserved: true,
      toolKind: "read",
      approvalMode: "auto-approve",
      expectedDecision: "approve",
      outcome: "executed",
      toolCallId: "call_packaged_agent_read",
    });
    expect(result.observation).toContain("PACKAGED_AGENT_TOOL_OBSERVATION");
    expect(readSnapshot).toHaveBeenCalledTimes(2);
  });

  it("does not accept an executed call without terminal backend usage", async () => {
    const clock = fakeClock();

    await expect(
      waitForAgentToolRoundCompletion({
        expectedToolCallId: "call_packaged_agent_read",
        expectedObservation: "PACKAGED_AGENT_TOOL_OBSERVATION",
        expectedFinalAssistant: "PACKAGED_AGENT_TOOL_ROUND_COMPLETE",
        readSnapshot: () => ({
          ...completedSnapshot,
          executionUsage: undefined,
        }),
        timeoutMs: 100,
        pollIntervalMs: 25,
        ...clock,
      }),
    ).rejects.toThrow(/terminal backend usage was not observed/);
  });

  it("requires an irreversible external receipt for an executed run", async () => {
    const clock = fakeClock();
    const result = await waitForAgentToolRoundCompletionCore({
      toolKind: "run",
      expectedUsageOperation: "run",
      approvalMode: "ask",
      expectedDecision: "approve",
      expectedOutcome: "executed",
      expectedToolCallId: "call_packaged_agent_run_approve",
      expectedObservation: "PACKAGED_AGENT_TOOL_OBSERVATION",
      expectedFinalAssistant: "PACKAGED_AGENT_RUN_APPROVE_ROUND_COMPLETE",
      readSnapshot: () => completedRunSnapshot,
      timeoutMs: 100,
      pollIntervalMs: 25,
      ...clock,
    });

    expect(result).toMatchObject({
      externalReceiptId: "receipt-packaged-agent-run",
      externalReceiptReversible: false,
      externalCompensation: "not-needed",
    });
  });

  it("rejects an executed run without its external receipt", async () => {
    const clock = fakeClock();
    await expect(waitForAgentToolRoundCompletionCore({
      toolKind: "run",
      expectedUsageOperation: "run",
      approvalMode: "ask",
      expectedDecision: "approve",
      expectedOutcome: "executed",
      expectedToolCallId: "call_packaged_agent_run_approve",
      expectedObservation: "PACKAGED_AGENT_TOOL_OBSERVATION",
      expectedFinalAssistant: "PACKAGED_AGENT_RUN_APPROVE_ROUND_COMPLETE",
      readSnapshot: () => ({
        ...completedRunSnapshot,
        executionUsage: {
          ...completedRunSnapshot.executionUsage!,
          externalReceiptId: "",
        },
      }),
      timeoutMs: 100,
      pollIntervalMs: 25,
      ...clock,
    })).rejects.toThrow(/irreversible external receipt/);
  });

  it("rejects a receipt that does not belong to the execution request session", async () => {
    const clock = fakeClock();

    await expect(
      waitForAgentToolRoundCompletion({
        expectedToolCallId: "call_packaged_agent_read",
        expectedObservation: "PACKAGED_AGENT_TOOL_OBSERVATION",
        expectedFinalAssistant: "PACKAGED_AGENT_TOOL_ROUND_COMPLETE",
        readSnapshot: () => ({
          ...completedSnapshot,
          executionUsage: {
            ...completedSnapshot.executionUsage!,
            sessionMatchesRequest: false,
          },
        }),
        timeoutMs: 100,
        pollIntervalMs: 25,
        ...clock,
      }),
    ).rejects.toThrow(/terminal backend usage was not observed/);
  });

  it.each([
    ["an empty UnitID", { unitId: "" }],
    ["an empty session ID", { sessionId: "" }],
    ["the wrong operation", { operation: "write" }],
    ["an unsuccessful terminal record", { success: false }],
    ["a still-pending record", { pending: true }],
    ["a mismatched observation", { observationMatchesToolResult: false }],
  ])("rejects terminal backend usage with %s", async (_name, override) => {
    const clock = fakeClock();

    await expect(
      waitForAgentToolRoundCompletion({
        expectedToolCallId: "call_packaged_agent_read",
        expectedObservation: "PACKAGED_AGENT_TOOL_OBSERVATION",
        expectedFinalAssistant: "PACKAGED_AGENT_TOOL_ROUND_COMPLETE",
        readSnapshot: () => ({
          ...completedSnapshot,
          executionUsage: {
            ...completedSnapshot.executionUsage!,
            ...override,
          },
        }),
        timeoutMs: 100,
        pollIntervalMs: 25,
        ...clock,
      }),
    ).rejects.toThrow(
      /terminal backend usage (?:was not observed|operation was write; expected read)/,
    );
  });

  it("rejects approval evidence recorded after execution and observation", async () => {
    const clock = fakeClock();

    await expect(
      waitForAgentToolRoundCompletion({
        expectedToolCallId: "call_packaged_agent_read",
        expectedObservation: "PACKAGED_AGENT_TOOL_OBSERVATION",
        expectedFinalAssistant: "PACKAGED_AGENT_TOOL_ROUND_COMPLETE",
        readSnapshot: () => ({
          ...completedSnapshot,
          timeline: [
            { toolCallId: "call_packaged_agent_read", stage: "executing", status: "executing" },
            { toolCallId: "call_packaged_agent_read", stage: "result", status: "executed" },
            { toolCallId: "call_packaged_agent_read", stage: "observation", status: "observation" },
            { toolCallId: "call_packaged_agent_read", stage: "approval", status: "approved" },
          ],
        }),
        timeoutMs: 100,
        pollIntervalMs: 25,
        ...clock,
      }),
    ).rejects.toThrow(/approved -> executing -> executed -> observation in order/);
  });

  it("does not accept a final assistant message without the tool execution chain", async () => {
    const clock = fakeClock();

    await expect(
      waitForAgentToolRoundCompletion({
        expectedToolCallId: "call_packaged_agent_read",
        expectedObservation: "PACKAGED_AGENT_TOOL_OBSERVATION",
        expectedFinalAssistant: "PACKAGED_AGENT_TOOL_ROUND_COMPLETE",
        readSnapshot: () => ({
          ...completedSnapshot,
          toolCalls: [],
          messages: [
            { role: "assistant", content: "PACKAGED_AGENT_TOOL_ROUND_COMPLETE" },
          ],
        }),
        timeoutMs: 100,
        pollIntervalMs: 25,
        ...clock,
      }),
    ).rejects.toThrow(/native read tool call was not observed/);
  });

  it("rejects a text observation that does not preserve the provider call ID", async () => {
    const clock = fakeClock();

    await expect(
      waitForAgentToolRoundCompletion({
        expectedToolCallId: "call_packaged_agent_read",
        expectedObservation: "PACKAGED_AGENT_TOOL_OBSERVATION",
        expectedFinalAssistant: "PACKAGED_AGENT_TOOL_ROUND_COMPLETE",
        readSnapshot: () => ({
          ...completedSnapshot,
          messages: [
            { role: "user", content: "Read the packaged fixture." },
            { role: "user", content: "[Observation]\nPACKAGED_AGENT_TOOL_OBSERVATION" },
            { role: "assistant", content: "PACKAGED_AGENT_TOOL_ROUND_COMPLETE" },
          ],
        }),
        timeoutMs: 100,
        pollIntervalMs: 25,
        ...clock,
      }),
    ).rejects.toThrow(/provider-native assistant tool call/);
  });

  it("rejects a structured result for another provider call ID", async () => {
    const clock = fakeClock();

    await expect(
      waitForAgentToolRoundCompletion({
        expectedToolCallId: "call_packaged_agent_read",
        expectedObservation: "PACKAGED_AGENT_TOOL_OBSERVATION",
        expectedFinalAssistant: "PACKAGED_AGENT_TOOL_ROUND_COMPLETE",
        readSnapshot: () => ({
          ...completedSnapshot,
          messages: completedSnapshot.messages.map((message) =>
            message.role === "tool"
              ? {
                  ...message,
                  toolResults: [{
                    toolCallId: "call_wrong",
                    content: "PACKAGED_AGENT_TOOL_OBSERVATION",
                  }],
                }
              : message),
        }),
        timeoutMs: 100,
        pollIntervalMs: 25,
        ...clock,
      }),
    ).rejects.toThrow(/provider-native tool result/);
  });

  it("fails immediately when the backend tool execution reaches an error state", async () => {
    const clock = fakeClock();

    await expect(
      waitForAgentToolRoundCompletion({
        expectedToolCallId: "call_packaged_agent_read",
        expectedObservation: "PACKAGED_AGENT_TOOL_OBSERVATION",
        expectedFinalAssistant: "PACKAGED_AGENT_TOOL_ROUND_COMPLETE",
        readSnapshot: () => ({
          ...completedSnapshot,
          toolCalls: [
            {
              id: "call_packaged_agent_read",
              kind: "read",
              status: "error",
              error: "backend capability was rejected",
            },
          ],
        }),
        timeoutMs: 1_000,
        pollIntervalMs: 25,
        ...clock,
      }),
    ).rejects.toThrow(/backend capability was rejected/);
  });

  it("accepts a search auto-approval round with terminal search usage", async () => {
    const clock = fakeClock();
    const searchSnapshot: AgentToolRoundSnapshot = {
      ...completedSnapshot,
      executionUsage: {
        ...completedSnapshot.executionUsage!,
        unitId: "unit-packaged-agent-search",
        operation: "search",
      },
      messages: [
        { role: "user", content: "Search the packaged workspace." },
        {
          role: "assistant",
          content: "",
          toolCalls: [{
            id: "call_packaged_agent_search",
            name: "search",
            arguments: '{"query":"PACKAGED_AGENT_SEARCH_OBSERVATION"}',
          }],
        },
        {
          role: "tool",
          content: "PACKAGED_AGENT_SEARCH_OBSERVATION",
          toolResults: [{
            toolCallId: "call_packaged_agent_search",
            content: "PACKAGED_AGENT_SEARCH_OBSERVATION",
            isError: false,
          }],
        },
        { role: "assistant", content: "PACKAGED_AGENT_SEARCH_COMPLETE" },
      ],
      toolCalls: [{
        id: "call_packaged_agent_search",
        kind: "search",
        status: "executed",
        result: "PACKAGED_AGENT_SEARCH_OBSERVATION",
      }],
      timeline: completedSnapshot.timeline.map((entry) => ({
        ...entry,
        toolCallId: "call_packaged_agent_search",
      })),
    };

    const result = await waitForAgentToolRoundCompletionCore({
      toolKind: "search",
      expectedUsageOperation: "search",
      approvalMode: "auto-approve",
      expectedDecision: "approve",
      expectedOutcome: "executed",
      expectedToolCallId: "call_packaged_agent_search",
      expectedObservation: "PACKAGED_AGENT_SEARCH_OBSERVATION",
      expectedFinalAssistant: "PACKAGED_AGENT_SEARCH_COMPLETE",
      readSnapshot: () => searchSnapshot,
      timeoutMs: 100,
      pollIntervalMs: 25,
      ...clock,
    });

    expect(result).toMatchObject({
      toolKind: "search",
      approvalMode: "auto-approve",
      usageOperation: "search",
      toolCallId: "call_packaged_agent_search",
    });
  });

  it("preserves the ask approval contract after an external approval completes", async () => {
    const clock = fakeClock();

    const result = await waitForAgentToolRoundCompletionCore({
      toolKind: "read",
      expectedUsageOperation: "read",
      approvalMode: "ask",
      expectedDecision: "approve",
      expectedOutcome: "executed",
      expectedToolCallId: "call_packaged_agent_read",
      expectedObservation: "PACKAGED_AGENT_TOOL_OBSERVATION",
      expectedFinalAssistant: "PACKAGED_AGENT_TOOL_ROUND_COMPLETE",
      readSnapshot: () => completedSnapshot,
      timeoutMs: 100,
      pollIntervalMs: 25,
      ...clock,
    });

    expect(result.approvalMode).toBe("ask");
  });

  it("rejects an executed tool whose kind does not match the round contract", async () => {
    const clock = fakeClock();

    await expect(
      waitForAgentToolRoundCompletionCore({
        toolKind: "search",
        expectedUsageOperation: "search",
        approvalMode: "auto-approve",
        expectedDecision: "approve",
        expectedOutcome: "executed",
        expectedToolCallId: "call_packaged_agent_read",
        expectedObservation: "PACKAGED_AGENT_TOOL_OBSERVATION",
        expectedFinalAssistant: "PACKAGED_AGENT_TOOL_ROUND_COMPLETE",
        readSnapshot: () => ({
          ...completedSnapshot,
          executionUsage: {
            ...completedSnapshot.executionUsage!,
            operation: "search",
          },
        }),
        timeoutMs: 100,
        pollIntervalMs: 25,
        ...clock,
      }),
    ).rejects.toThrow(/native tool kind was read; expected search/);
  });

  it("rejects a terminal usage operation that does not match the search contract", async () => {
    const clock = fakeClock();
    const searchToolSnapshot: AgentToolRoundSnapshot = {
      ...completedSnapshot,
      toolCalls: [{
        ...completedSnapshot.toolCalls[0]!,
        kind: "search",
      }],
      messages: completedSnapshot.messages.map((entry) =>
        entry.toolCalls
          ? {
              ...entry,
              toolCalls: entry.toolCalls.map((call) => ({
                ...call,
                name: "search",
              })),
            }
          : entry),
    };

    await expect(
      waitForAgentToolRoundCompletionCore({
        toolKind: "search",
        expectedUsageOperation: "search",
        approvalMode: "auto-approve",
        expectedDecision: "approve",
        expectedOutcome: "executed",
        expectedToolCallId: "call_packaged_agent_read",
        expectedObservation: "PACKAGED_AGENT_TOOL_OBSERVATION",
        expectedFinalAssistant: "PACKAGED_AGENT_TOOL_ROUND_COMPLETE",
        readSnapshot: () => searchToolSnapshot,
        timeoutMs: 100,
        pollIntervalMs: 25,
        ...clock,
      }),
    ).rejects.toThrow(/terminal backend usage operation was read; expected search/);
  });

  it("accepts a manual native rejection only with isError and no execution usage", async () => {
    const clock = fakeClock();
    const rejection = 'User rejected the write action on "fixture.txt".';
    const rejectedSnapshot: AgentToolRoundSnapshot = {
      streaming: false,
      globalStreamBusy: false,
      error: null,
      messages: [
        { role: "user", content: "Write the fixture." },
        {
          role: "assistant",
          content: "",
          toolCalls: [{
            id: "call_packaged_agent_write_reject",
            name: "write",
            arguments: '{"path":"fixture.txt","content":"changed"}',
          }],
        },
        {
          role: "tool",
          content: rejection,
          toolResults: [{
            toolCallId: "call_packaged_agent_write_reject",
            content: rejection,
            isError: true,
          }],
        },
        { role: "assistant", content: "PACKAGED_AGENT_WRITE_REJECT_COMPLETE" },
      ],
      toolCalls: [{
        id: "call_packaged_agent_write_reject",
        kind: "write",
        status: "rejected",
      }],
      timeline: [
        { toolCallId: "call_packaged_agent_write_reject", stage: "requested", status: "pending" },
        { toolCallId: "call_packaged_agent_write_reject", stage: "approval", status: "waiting-approval" },
        { toolCallId: "call_packaged_agent_write_reject", stage: "result", status: "rejected" },
        { toolCallId: "call_packaged_agent_write_reject", stage: "observation", status: "observation" },
      ],
    };

    const result = await waitForAgentToolRoundCompletionCore({
      toolKind: "write",
      expectedUsageOperation: "write",
      approvalMode: "ask",
      expectedDecision: "reject",
      expectedOutcome: "rejected",
      expectedToolCallId: "call_packaged_agent_write_reject",
      expectedObservation: "User rejected the write action",
      expectedFinalAssistant: "PACKAGED_AGENT_WRITE_REJECT_COMPLETE",
      readSnapshot: () => rejectedSnapshot,
      timeoutMs: 100,
      pollIntervalMs: 25,
      ...clock,
    });

    expect(result).toMatchObject({
      toolKind: "write",
      expectedDecision: "reject",
      outcome: "rejected",
      decisionObserved: true,
      approvalObserved: false,
      backendExecutionObserved: false,
      executionUsageObserved: false,
      observationSubmitted: false,
      rejectionSubmitted: true,
      nativeProtocolResultSubmitted: true,
    });
    expect(result.rejection).toContain("User rejected the write action");
    expect(result).not.toHaveProperty("usageUnitId");

    await expect(
      waitForAgentToolRoundCompletionCore({
        toolKind: "write",
        expectedUsageOperation: "write",
        approvalMode: "ask",
        expectedDecision: "reject",
        expectedOutcome: "rejected",
        expectedToolCallId: "call_packaged_agent_write_reject",
        expectedObservation: "User rejected the write action",
        expectedFinalAssistant: "PACKAGED_AGENT_WRITE_REJECT_COMPLETE",
        readSnapshot: () => ({
          ...rejectedSnapshot,
          executionUsage: {
            unitId: "unexpected-unit",
            sessionId: "unexpected-session",
            operation: "write",
            success: false,
            pending: false,
            sessionMatchesRequest: true,
            observationMatchesToolResult: false,
          },
        }),
        timeoutMs: 100,
        pollIntervalMs: 25,
        ...fakeClock(),
      }),
    ).rejects.toThrow(/unexpectedly retained backend execution usage/);
  });

  it("clicks the matching enabled manual control through HTMLElement.click", async () => {
    const host = document.createElement("div");
    host.innerHTML = `
      <article data-agent-tool-call-id="call_manual_run" data-agent-tool-kind="run" data-agent-tool-status="pending">
        <button type="button" data-agent-tool-action="approve" data-agent-tool-call-id="call_manual_run" data-agent-tool-kind="run">Approve</button>
      </article>`;
    document.body.append(host);
    const observed = vi.fn();
    host.querySelector("button")?.addEventListener("click", observed);

    const evidence = await clickManualAgentToolDecision({
      root: host,
      toolKind: "run",
      expectedToolCallId: "call_manual_run",
      expectedDecision: "approve",
      readSnapshot: () => ({
        streaming: false,
        globalStreamBusy: false,
        error: null,
        messages: [],
        toolCalls: [{ id: "call_manual_run", kind: "run", status: "pending" }],
        timeline: [],
      }),
      timeoutMs: 100,
      pollIntervalMs: 25,
      ...fakeClock(),
    });

    expect(observed).toHaveBeenCalledOnce();
    expect(evidence).toEqual({
      manualControlRendered: true,
      manualControlClicked: true,
      manualControlClickEventObserved: true,
      manualControlWasEnabled: true,
      manualControlAction: "approve",
      manualControlCallId: "call_manual_run",
      manualControlKind: "run",
    });
    host.remove();
  });

  it("fails closed for a wrong manual tool kind", async () => {
    await expect(
      clickManualAgentToolDecision({
        root: document,
        toolKind: "write",
        expectedToolCallId: "call_wrong_kind",
        expectedDecision: "reject",
        readSnapshot: () => ({
          streaming: false,
          globalStreamBusy: false,
          error: null,
          messages: [],
          toolCalls: [{ id: "call_wrong_kind", kind: "run", status: "pending" }],
          timeline: [],
        }),
        timeoutMs: 100,
        pollIntervalMs: 25,
        ...fakeClock(),
      }),
    ).rejects.toThrow(/tool kind run; expected write/);
  });

  it("fails closed when the real manual DOM button is missing", async () => {
    const host = document.createElement("div");
    await expect(
      clickManualAgentToolDecision({
        root: host,
        toolKind: "write",
        expectedToolCallId: "call_missing_button",
        expectedDecision: "approve",
        readSnapshot: () => ({
          streaming: false,
          globalStreamBusy: false,
          error: null,
          messages: [],
          toolCalls: [{ id: "call_missing_button", kind: "write", status: "pending" }],
          timeline: [],
        }),
        timeoutMs: 100,
        pollIntervalMs: 25,
        ...fakeClock(),
      }),
    ).rejects.toThrow(/approve button was absent/);
  });

  it("fails closed rather than clicking a disabled manual DOM control", async () => {
    const host = document.createElement("div");
    host.innerHTML = `
      <article data-agent-tool-call-id="call_disabled" data-agent-tool-kind="run" data-agent-tool-status="pending">
        <button disabled type="button" data-agent-tool-action="reject" data-agent-tool-call-id="call_disabled" data-agent-tool-kind="run">Reject</button>
      </article>`;
    const observed = vi.fn();
    host.querySelector("button")?.addEventListener("click", observed);

    await expect(
      clickManualAgentToolDecision({
        root: host,
        toolKind: "run",
        expectedToolCallId: "call_disabled",
        expectedDecision: "reject",
        readSnapshot: () => ({
          streaming: false,
          globalStreamBusy: false,
          error: null,
          messages: [],
          toolCalls: [{ id: "call_disabled", kind: "run", status: "pending" }],
          timeline: [],
        }),
        timeoutMs: 100,
        pollIntervalMs: 25,
        ...fakeClock(),
      }),
    ).rejects.toThrow(/remained disabled/);
    expect(observed).not.toHaveBeenCalled();
  });
});
