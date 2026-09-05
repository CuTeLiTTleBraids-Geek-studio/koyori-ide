import { beforeEach, describe, expect, it } from "vitest";
import {
  __resetAgentTimelineForTests,
  agentTimelineState,
  MAX_PROVIDER_REASONING_SUMMARY_BYTES,
  recordProviderReasoningSummary,
  recordToolObservation,
  recordToolRequested,
  recordToolStage,
  restoreAgentTimelineFromMessages,
  resetAgentTimeline,
} from "./agentTimeline";

describe("agent execution timeline", () => {
  beforeEach(() => {
    __resetAgentTimelineForTests();
  });

  it("records requested, approval, execution, result, and observation milestones", () => {
    recordToolRequested("call-1", "read", "src/main.ts");
    recordToolStage("call-1", "read", "waiting-approval");
    recordToolStage("call-1", "read", "approved");
    recordToolStage("call-1", "read", "executing");
    recordToolStage("call-1", "read", "executed", "read 12 lines");
    recordToolObservation("call-1", "read", "read 12 lines");

    expect(agentTimelineState.entries.map((entry) => entry.stage)).toEqual([
      "requested",
      "approval",
      "approval",
      "executing",
      "result",
      "observation",
    ]);
    expect(agentTimelineState.entries.at(-1)?.detail).toBe("read 12 lines");
  });

  it("places rejected calls in the result phase", () => {
    recordToolRequested("call-rejected", "write", "src/main.ts");
    recordToolStage("call-rejected", "write", "rejected", "Approval rejected");
    expect(agentTimelineState.entries.at(-1)).toMatchObject({
      stage: "result",
      status: "rejected",
    });
  });

  it("records only explicit, non-empty provider summaries as reasoning", () => {
    recordProviderReasoningSummary(" ");
    expect(agentTimelineState.entries).toHaveLength(0);

    recordProviderReasoningSummary("I will inspect the failing test first.");
    expect(agentTimelineState.entries[0]).toMatchObject({
      kind: "reasoning",
      stage: "reasoning",
      explicit: true,
      detail: "I will inspect the failing test first.",
    });

    recordToolObservation(
      "call-ordinary-content",
      "read",
      "Ordinary assistant content must stay a tool observation.",
    );
    const ordinaryContentEntry = agentTimelineState.entries[1];
    expect(ordinaryContentEntry).toMatchObject({
      kind: "tool",
      stage: "observation",
    });
    expect(ordinaryContentEntry.explicit).toBeUndefined();
  });

  it("aggregates consecutive provider summary deltas in place", () => {
    const first = recordProviderReasoningSummary("Checking");
    const originalId = first?.id;
    const originalCreatedAt = first?.createdAt;
    const originalUpdatedAt = first?.updatedAt;
    const second = recordProviderReasoningSummary(" files");
    const third = recordProviderReasoningSummary(" and tests.");

    expect(second).toBe(first);
    expect(third).toBe(first);
    expect(agentTimelineState.entries).toHaveLength(1);
    expect(first).toMatchObject({
      kind: "reasoning",
      explicit: true,
      detail: "Checking files and tests.",
    });
    expect(first?.id).toBe(originalId);
    expect(first?.createdAt).toBe(originalCreatedAt);
    expect(first!.updatedAt).toBeGreaterThan(originalUpdatedAt!);
  });

  it.each([
    [
      "request",
      () => recordToolRequested("call-request-boundary", "read", "src/main.ts"),
      "requested",
    ],
    [
      "stage",
      () => recordToolStage("call-stage-boundary", "run", "executing"),
      "executing",
    ],
    [
      "observation",
      () => recordToolObservation("call-observation-boundary", "search", "matched one file"),
      "observation",
    ],
  ])("starts a new provider summary after a tool %s boundary", (_name, boundary, stage) => {
    const beforeTool = recordProviderReasoningSummary("Before tool");
    boundary();
    const afterTool = recordProviderReasoningSummary("After tool");

    expect(afterTool).not.toBe(beforeTool);
    expect(agentTimelineState.entries.map((entry) => entry.stage)).toEqual([
      "reasoning",
      stage,
      "reasoning",
    ]);
    expect(afterTool?.detail).toBe("After tool");
  });

  it("starts a new provider summary after reset", () => {
    const beforeReset = recordProviderReasoningSummary("Old turn");
    resetAgentTimeline();
    const afterReset = recordProviderReasoningSummary("New turn");

    expect(afterReset).not.toBe(beforeReset);
    expect(agentTimelineState.entries).toHaveLength(1);
    expect(agentTimelineState.entries[0].detail).toBe("New turn");
  });

  it("rebuilds persisted request, result, and observation without inventing approval", () => {
    restoreAgentTimelineFromMessages([
      {
        toolCalls: [
          { id: "call-read", name: "read", arguments: '{"path":"README.md"}' },
          { id: "call-run", name: "run", arguments: '{"command":"go version"}' },
        ],
      },
      {
        toolResults: [
          { toolCallId: "call-read", content: "file body" },
          { toolCallId: "call-run", content: "process failed", isError: true },
          { toolCallId: "unknown", content: "must not be projected" },
        ],
      },
    ]);

    expect(agentTimelineState.entries.map((entry) => entry.stage)).toEqual([
      "requested",
      "requested",
      "result",
      "observation",
      "result",
      "observation",
    ]);
    expect(agentTimelineState.entries.some((entry) => entry.stage === "approval")).toBe(false);
    expect(agentTimelineState.entries[0]).toMatchObject({
      toolCallId: "call-read",
      tool: "read",
      target: "README.md",
    });
    expect(agentTimelineState.entries.at(-2)).toMatchObject({
      toolCallId: "call-run",
      status: "error",
      detail: "process failed",
    });
    expect(agentTimelineState.entries.some((entry) => entry.toolCallId === "unknown")).toBe(false);
  });

  it("accepts an exact UTF-8 byte boundary and bounds the next delta", () => {
    recordProviderReasoningSummary("a".repeat(MAX_PROVIDER_REASONING_SUMMARY_BYTES));
    expect(agentTimelineState.entries[0].detail).toBe(
      "a".repeat(MAX_PROVIDER_REASONING_SUMMARY_BYTES),
    );

    recordProviderReasoningSummary("b");
    const detail = agentTimelineState.entries[0].detail ?? "";
    expect(new TextEncoder().encode(detail).byteLength).toBeLessThanOrEqual(
      MAX_PROVIDER_REASONING_SUMMARY_BYTES,
    );
    expect(detail.endsWith("\n…")).toBe(true);
  });

  it("truncates oversized Unicode summaries without splitting a code point", () => {
    recordProviderReasoningSummary("你好🙂".repeat(MAX_PROVIDER_REASONING_SUMMARY_BYTES));

    const detail = agentTimelineState.entries[0].detail ?? "";
    expect(new TextEncoder().encode(detail).byteLength).toBeLessThanOrEqual(
      MAX_PROVIDER_REASONING_SUMMARY_BYTES,
    );
    expect(detail.endsWith("\n…")).toBe(true);
    expect(detail).not.toContain("\uFFFD");
    const withoutSuffix = detail.slice(0, -2);
    expect(withoutSuffix.endsWith("🙂") || withoutSuffix.endsWith("好") || withoutSuffix.endsWith("你")).toBe(true);
  });

  it("bounds retained history and deduplicates repeated tool stages", () => {
    recordToolRequested("call-2", "search", "needle");
    recordToolRequested("call-2", "search", "needle");
    recordToolStage("call-2", "search", "executing");
    recordToolStage("call-2", "search", "executing");
    expect(agentTimelineState.entries).toHaveLength(2);

    for (let index = 0; index < 300; index += 1) {
      recordToolRequested(`bounded-call-${index}`, "read", `file-${index}.ts`);
    }
    expect(agentTimelineState.entries.length).toBeLessThanOrEqual(240);
  });
});
