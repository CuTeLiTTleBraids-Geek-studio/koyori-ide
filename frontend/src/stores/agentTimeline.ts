import { reactive, watch, type WatchStopHandle } from "vue";

/** Renderer-visible execution records. Hidden chain-of-thought is never inferred. */
export type AgentTimelineStage =
  | "reasoning"
  | "requested"
  | "approval"
  | "executing"
  | "result"
  | "observation";
export type AgentTimelineKind = "reasoning" | "tool";

export interface AgentTimelineEntry {
  id: string;
  kind: AgentTimelineKind;
  stage: AgentTimelineStage;
  explicit?: boolean;
  createdAt: number;
  updatedAt: number;
  toolCallId?: string;
  tool?: string;
  target?: string;
  detail?: string;
  status?: string;
}
export interface AgentTimelineState { entries: AgentTimelineEntry[] }

interface PersistedTimelineToolCall {
  id: string;
  name: string;
  arguments?: string;
}

interface PersistedTimelineToolResult {
  toolCallId: string;
  content: string;
  isError?: boolean;
}

interface PersistedTimelineMessage {
  toolCalls?: PersistedTimelineToolCall[];
  toolResults?: PersistedTimelineToolResult[];
}

const MAX_ENTRIES = 240;
const MAX_DETAIL_LENGTH = 1200;
export const MAX_PROVIDER_REASONING_SUMMARY_BYTES = 1200;
const REASONING_SUMMARY_TRUNCATION = "\n…";
export const agentTimelineState = reactive<AgentTimelineState>({ entries: [] });

let entryCounter = 0;
let boundStop: WatchStopHandle | null = null;
let activeProviderReasoningEntry: AgentTimelineEntry | null = null;
let activeProviderReasoningTruncated = false;
const lastStages = new Map<string, AgentTimelineStage>();
const lastStatuses = new Map<string, string>();

const utf8Encoder = new TextEncoder();

function utf8Length(value: string): number {
  return utf8Encoder.encode(value).byteLength;
}

function boundProviderReasoningSummary(value: string): { detail: string; truncated: boolean } {
  if (utf8Length(value) <= MAX_PROVIDER_REASONING_SUMMARY_BYTES) {
    return { detail: value, truncated: false };
  }

  const contentBudget = Math.max(
    0,
    MAX_PROVIDER_REASONING_SUMMARY_BYTES - utf8Length(REASONING_SUMMARY_TRUNCATION),
  );
  const prefix: string[] = [];
  let bytes = 0;
  // Iterating a string yields complete Unicode code points, so the retained
  // prefix cannot end in the middle of a UTF-8 sequence or surrogate pair.
  for (const codePoint of value) {
    const codePointBytes = utf8Length(codePoint);
    if (bytes + codePointBytes > contentBudget) break;
    prefix.push(codePoint);
    bytes += codePointBytes;
  }
  return {
    detail: `${prefix.join("")}${REASONING_SUMMARY_TRUNCATION}`,
    truncated: true,
  };
}

function closeProviderReasoningAggregation(): void {
  activeProviderReasoningEntry = null;
  activeProviderReasoningTruncated = false;
}

function trimDetail(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  const detail = value.trim();
  if (!detail) return undefined;
  return detail.length <= MAX_DETAIL_LENGTH ? detail : `${detail.slice(0, MAX_DETAIL_LENGTH)}\n…`;
}

function appendEntry(entry: Omit<AgentTimelineEntry, "id" | "createdAt" | "updatedAt">): AgentTimelineEntry {
  entryCounter += 1;
  const now = Date.now();
  const result: AgentTimelineEntry = { ...entry, id: `agent-timeline-${now.toString(36)}-${entryCounter}`, createdAt: now, updatedAt: now };
  agentTimelineState.entries.push(result);
  if (agentTimelineState.entries.length > MAX_ENTRIES) agentTimelineState.entries.splice(0, agentTimelineState.entries.length - MAX_ENTRIES);
  return result;
}

export function appendAgentTimeline(stage: AgentTimelineStage, detail?: string, metadata: Pick<AgentTimelineEntry, "toolCallId" | "tool" | "target" | "status"> = {}): AgentTimelineEntry {
  // Generic renderer activity is a boundary, never another provider summary
  // delta. Keeping the legacy API separate prevents ordinary content from
  // being folded into the provider-declared summary entry.
  closeProviderReasoningAggregation();
  return appendEntry({ kind: stage === "reasoning" ? "reasoning" : "tool", stage, explicit: stage === "reasoning" ? true : undefined, detail: trimDetail(detail), ...metadata });
}

export function recordProviderReasoningSummary(summary: string): AgentTimelineEntry | null {
  // This API is intentionally fed only by the provider-declared
  // `ai:reasoning` event. It never parses assistant content or raw thinking.
  const activeEntry = activeProviderReasoningEntry &&
    agentTimelineState.entries.includes(activeProviderReasoningEntry)
    ? activeProviderReasoningEntry
    : null;
  if (!activeEntry) closeProviderReasoningAggregation();
  const fragment = activeEntry ? summary : summary.trim();
  if (!fragment) return null;

  if (activeEntry) {
    if (!activeProviderReasoningTruncated) {
      const bounded = boundProviderReasoningSummary(`${activeEntry.detail ?? ""}${fragment}`);
      activeEntry.detail = bounded.detail;
      activeProviderReasoningTruncated = bounded.truncated;
    }
    activeEntry.updatedAt = Math.max(Date.now(), activeEntry.updatedAt + 1);
    return activeEntry;
  }

  closeProviderReasoningAggregation();
  const bounded = boundProviderReasoningSummary(fragment);
  const entry = appendEntry({
    kind: "reasoning",
    stage: "reasoning",
    explicit: true,
    detail: bounded.detail,
  });
  activeProviderReasoningEntry = entry;
  activeProviderReasoningTruncated = bounded.truncated;
  return entry;
}

function historicalToolTarget(argumentsValue?: string): string | undefined {
  if (!argumentsValue) return undefined;
  try {
    const parsed = JSON.parse(argumentsValue) as unknown;
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return undefined;
    const values = parsed as Record<string, unknown>;
    for (const key of ["path", "command", "query", "tool", "skillId", "workflowId"]) {
      const value = values[key];
      if (typeof value === "string") return trimDetail(value);
    }
  } catch {
    // Persisted provider arguments may be malformed; history stays fail-closed.
  }
  return undefined;
}

/**
 * Rebuilds only facts carried by persisted native protocol messages. A tool
 * result proves a result/observation existed, but it does not prove how (or
 * whether) a human approval was obtained, so approval entries are never
 * synthesized here.
 */
export function restoreAgentTimelineFromMessages(messages: PersistedTimelineMessage[]): void {
  resetAgentTimeline();
  const knownTools = new Map<string, string>();
  const seenCalls = new Set<string>();
  for (const message of messages) {
    for (const call of message.toolCalls ?? []) {
      const id = call.id?.trim();
      const tool = call.name?.trim();
      if (!id || !tool || seenCalls.has(id)) continue;
      seenCalls.add(id);
      knownTools.set(id, tool);
      recordToolRequested(id, tool, historicalToolTarget(call.arguments));
    }
    for (const result of message.toolResults ?? []) {
      const id = result.toolCallId?.trim();
      const tool = knownTools.get(id);
      if (!id || !tool) continue;
      const detail = typeof result.content === "string" ? result.content : "";
      recordToolStage(id, tool, result.isError ? "error" : "executed", detail);
      if (detail.trim()) recordToolObservation(id, tool, detail);
    }
  }
}

export function recordToolRequested(toolCallId: string, tool: string, target?: string): AgentTimelineEntry | null {
  if (!toolCallId) return null;
  closeProviderReasoningAggregation();
  if (lastStages.get(toolCallId) === "requested") return null;
  lastStages.set(toolCallId, "requested");
  lastStatuses.set(toolCallId, "pending");
  return appendEntry({ kind: "tool", stage: "requested", toolCallId, tool, target, status: "pending" });
}

function stageForToolStatus(stage: string): AgentTimelineStage {
  if (stage === "executing") return "executing";
  if (stage === "executed" || stage === "rejected" || stage === "error") return "result";
  return "approval";
}

export function recordToolStage(toolCallId: string, tool: string, stage: string, detail?: string): AgentTimelineEntry | null {
  if (!toolCallId) return null;
  closeProviderReasoningAggregation();
  const mapped = stageForToolStatus(stage);
  if (lastStages.get(toolCallId) === mapped && lastStatuses.get(toolCallId) === stage) return null;
  lastStages.set(toolCallId, mapped);
  lastStatuses.set(toolCallId, stage);
  return appendEntry({ kind: "tool", stage: mapped, toolCallId, tool, detail: trimDetail(detail), status: stage });
}

export function recordToolObservation(toolCallId: string, tool: string, detail: string): AgentTimelineEntry | null {
  if (!toolCallId) return null;
  closeProviderReasoningAggregation();
  lastStages.set(toolCallId, "observation");
  if (lastStatuses.get(toolCallId) === detail) return null;
  lastStatuses.set(toolCallId, detail);
  return appendEntry({ kind: "tool", stage: "observation", toolCallId, tool, detail: trimDetail(detail), status: "observation" });
}

/** Optional state binding for hosts that observe legacy direct state mutations. */
export function bindAgentState(state: { pendingToolCalls: Array<{ id: string; kind: string; target?: string; status: string; result?: string; error?: string }>; toolCallCount: number }): void {
  boundStop?.();
  let previous = new Map<string, { status: string; result?: string; error?: string }>();
  boundStop = watch(
    () => [state.toolCallCount, ...state.pendingToolCalls.map((call) => `${call.id}\0${call.kind}\0${call.target ?? ""}\0${call.status}\0${call.result ?? ""}\0${call.error ?? ""}`)],
    () => {
      if (state.toolCallCount === 0 && state.pendingToolCalls.length === 0) { resetAgentTimeline(); previous = new Map(); return; }
      const current = new Map(state.pendingToolCalls.map((call) => [call.id, call]));
      for (const call of state.pendingToolCalls) {
        const before = previous.get(call.id);
        if (!before) { recordToolRequested(call.id, call.kind, call.target); recordToolStage(call.id, call.kind, "pending"); }
        else if (before.status !== call.status) {
          if (call.status === "approved") { recordToolStage(call.id, call.kind, "approved"); recordToolStage(call.id, call.kind, "executing"); }
          else if (call.status === "executed" || call.status === "error") { recordToolStage(call.id, call.kind, call.status, call.error); if (call.result) recordToolObservation(call.id, call.kind, call.result); }
          else if (call.status === "rejected") recordToolStage(call.id, call.kind, "rejected");
        } else if (call.status === "executed" && before.result !== call.result && call.result) recordToolObservation(call.id, call.kind, call.result);
      }
      previous = new Map(Array.from(current, ([id, call]) => [id, { status: call.status, result: call.result, error: call.error }]));
    },
    { flush: "sync" },
  );
}

export function resetAgentTimeline(): void { agentTimelineState.entries.splice(0, agentTimelineState.entries.length); closeProviderReasoningAggregation(); lastStages.clear(); lastStatuses.clear(); }
export function __resetAgentTimelineForTests(): void { resetAgentTimeline(); entryCounter = 0; boundStop?.(); boundStop = null; }
