// Koyori IDE 模块 · Ai；交互服务：AI 对话（AIService）、对话历史（ConversationService）。
// 喵，这是 Koyori IDE 的 Ai 模块（前端实现）~
import { reactive } from "vue";
import { Events } from "@wailsio/runtime";
import { aiService, conversationService, searchService } from "@/api/services";
import { appState, activeAIConfig } from "@/stores/app";
import { notifyError } from "@/lib/notifications";
import { pushOutput } from "@/stores/output";
import {
  agentState,
  getAgentSystemPrompt,
  onAssistantFinished,
  onNativeToolCalls,
  buildNativeToolDefs,
	refreshAgentToolCatalog,
	ensureAgentSession,
	clearPendingToolCalls,
  type NativeToolCallPayload,
} from "@/stores/agent";
import {
  recordProviderReasoningSummary,
  restoreAgentTimelineFromMessages,
} from "@/stores/agentTimeline";
import { rulesForPrompt } from "@/stores/rules";
import { activePlan } from "@/stores/aiPlan";
import { activePersona } from "@/stores/persona";
import { errorMessage } from "@/lib/errors";
import { translate } from "@/lib/i18n";
import {
  getWindowOriginId,
  unwrapEventData,
  parseSyncOrigin,
} from "@/lib/windowOrigin";
import type {
  ChatMessage,
  AIContextAttachment,
  AIActionName,
  FileContextEntry,
  ContextChip,
  NativeToolCallContext,
  NativeToolResultContext,
} from "@/types";

export type AIStoreMessage = ChatMessage & { id: string };

function createMessage(
  role: ChatMessage["role"],
  content: string,
  images?: string[],
  toolCalls?: NativeToolCallContext[],
  toolResults?: NativeToolResultContext[],
): AIStoreMessage {
  return {
    role,
    content,
    ...(images && images.length > 0 ? { images } : {}),
    ...(toolCalls && toolCalls.length > 0 ? { toolCalls } : {}),
    ...(toolResults && toolResults.length > 0 ? { toolResults } : {}),
    id: crypto.randomUUID(),
  };
}

const INTERRUPTED_NATIVE_TOOL_RESULT =
  "Tool execution was interrupted before completion. No approval or execution authority was restored.";

function restoreInterruptedNativeToolRound(
  messages: AIStoreMessage[],
): AIStoreMessage[] {
  const pending = new Map<string, NativeToolCallContext>();
  for (const message of messages) {
    for (const call of message.toolCalls ?? []) pending.set(call.id, call);
    for (const result of message.toolResults ?? []) pending.delete(result.toolCallId);
  }
  if (pending.size === 0) return messages;

  return [
    ...messages,
    createMessage(
      "tool",
      INTERRUPTED_NATIVE_TOOL_RESULT,
      undefined,
      undefined,
      [...pending.values()].map((call) => ({
        toolCallId: call.id,
        content: INTERRUPTED_NATIVE_TOOL_RESULT,
        isError: true,
      })),
    ),
  ];
}

export interface AIState {
  messages: AIStoreMessage[];
  streaming: boolean;
  /**
   * prompt-5 Task B / BUG-H1: process-wide stream busy flag from backend
   * `ai:stream-busy` events. True when ANY webview holds the global AI stream
   * (main chat or AI companion window). Used to disable Send in the idle window.
   * prompt-6 Task 5: only updated from backend events (never cleared locally).
   */
  globalStreamBusy: boolean;
  /**
   * prompt-6 Task 2: stream id returned by StartStream / carried on ai:* events.
   * Only chunks with matching streamId (or empty legacy payloads) are assembled.
   */
  activeStreamId: string | null;
  error: string | null;
  context: AIContextAttachment | null;
  currentConversationId: string | null;
  currentConversationTitle: string | null;
  /** prompt-7 Task C: last known disk revision for CAS. */
  conversationRevision: number;
  /**
   * prompt-7 Task C / BUG-H6: peer saved same conversation while we stream.
   * After stream ends we pull before next persist.
   */
  conversationStaleWhileStreaming: boolean;
  mentionedFiles: FileContextEntry[];
  // Plan 11 Task 3: unified context chips for @mention + paste. These are
  // serialized into the next user message by buildUserMessage and cleared
  // after send (alongside mentionedFiles). mentionedFiles is kept for
  // backward compatibility with existing callers (runAIAction, etc.).
  contextChips: ContextChip[];
  // N-60: Per-conversation system prompt override. When non-null, this
  // conversation uses a custom system prompt instead of the global
  // appState.aiSystemPrompt. Null means "use the global default".
  currentSystemPromptOverride: string | null;
}

export const aiState = reactive<AIState>({
  messages: [],
  streaming: false,
  globalStreamBusy: false,
  activeStreamId: null,
  error: null,
  context: null,
  currentConversationId: null,
  currentConversationTitle: null,
  conversationRevision: 0,
  conversationStaleWhileStreaming: false,
  mentionedFiles: [],
  contextChips: [],
  currentSystemPromptOverride: null,
});

// Track event listener cleanup
let eventListenersRegistered = false;
let pendingAssistantMessage: AIStoreMessage | null = null;
let pendingAssistantSawNativeToolEvent = false;
let streamGeneration = 0;
let conversationLoadGeneration = 0;
let activeConversationLoadGeneration: number | null = null;
let activeConversationLoadID: string | null = null;
const pendingConversationSavedRevisions = new Map<string, number | null>();
let conversationPersistTail: Promise<void> = Promise.resolve();
let sendAdmissionInFlight = false;

function isCurrentConversationLoadActive(): boolean {
  return activeConversationLoadGeneration !== null &&
    activeConversationLoadGeneration === conversationLoadGeneration;
}

/**
 * Events can be delivered by Wails before StartStream returns its authority
 * stream ID. They are untrusted until that ID is known, so retain only a
 * bounded, generation-scoped prefix and replay it after admission.
 */
export const MAX_PRE_ADMISSION_STREAM_EVENTS = 256;
export const MAX_PRE_ADMISSION_STREAM_BYTES = 256 * 1024;

type BufferedAIEventKind = "chunk" | "done" | "error" | "tool_calls" | "reasoning";
interface BufferedAIEvent {
  kind: BufferedAIEventKind;
  streamId: string;
  data: string;
  raw: unknown;
}
interface StreamAdmissionBuffer {
  generation: number;
  events: BufferedAIEvent[];
  bytes: number;
  poisoned: boolean;
}

let streamAdmissionBuffer: StreamAdmissionBuffer | null = null;

function clearStreamAdmissionBuffer(generation?: number): void {
  if (generation === undefined || streamAdmissionBuffer?.generation === generation) {
    streamAdmissionBuffer = null;
  }
}

function beginStreamAdmission(generation: number): void {
  streamAdmissionBuffer = { generation, events: [], bytes: 0, poisoned: false };
}

function serializeBufferedEvent(event: BufferedAIEvent): string | null {
  try {
    return JSON.stringify(event);
  } catch {
    return null;
  }
}

/** Returns true when the event was consumed by the pre-admission gate. */
function bufferPreAdmissionEvent(
  kind: BufferedAIEventKind,
  payload: ReturnType<typeof parseAIStreamPayload>,
): boolean {
  const admission = streamAdmissionBuffer;
  if (!admission || admission.generation !== streamGeneration) return false;
  // Legacy/no-ID events cannot be attributed to the returned authority.
  if (!payload.streamId || admission.poisoned) return true;

  const event: BufferedAIEvent = {
    kind,
    streamId: payload.streamId,
    data: payload.data,
    raw: payload.raw,
  };
  const serialized = serializeBufferedEvent(event);
  if (
    !serialized
    || admission.events.length >= MAX_PRE_ADMISSION_STREAM_EVENTS
    || admission.bytes + new TextEncoder().encode(serialized).byteLength > MAX_PRE_ADMISSION_STREAM_BYTES
  ) {
    // Never replay a prefix after a capacity or serialization failure.
    admission.events = [];
    admission.bytes = 0;
    admission.poisoned = true;
    return true;
  }
  // Snapshot JSON event payloads so mutable Wails objects cannot alter the
  // authorized replay after admission.
  const snapshot = JSON.parse(serialized) as BufferedAIEvent;
  admission.events.push(snapshot);
  admission.bytes += new TextEncoder().encode(serialized).byteLength;
  return true;
}

function takeStreamAdmission(
  generation: number,
  streamId: string,
): { events: BufferedAIEvent[]; poisoned: boolean } | null {
  const admission = streamAdmissionBuffer;
  if (!admission || admission.generation !== generation) return null;
  streamAdmissionBuffer = null;
  if (admission.poisoned || !streamId) {
    return { events: [], poisoned: admission.poisoned };
  }
  return {
    events: admission.events.filter((event) => event.streamId === streamId),
    poisoned: false,
  };
}

// M-16: 流式超时兜底定时器。若 ai:done/ai:error 在超时窗口内未到达，
// 强制清理 streaming 状态，避免后端静默丢失事件导致 UI 永久卡死。
// 导出常量便于测试引用（测试中配合 vi.useFakeTimers 推进时间）。
export const STREAM_TIMEOUT_MS = 5 * 60 * 1000; // 5 分钟
let streamTimeoutTimer: ReturnType<typeof setTimeout> | null = null;
let busyReconciliationGeneration = 0;
const BUSY_RECONCILIATION_ATTEMPTS = 20;
const BUSY_RECONCILIATION_DELAY_MS = 100;

function clearStreamTimeout(): void {
  if (streamTimeoutTimer !== null) {
    clearTimeout(streamTimeoutTimer);
    streamTimeoutTimer = null;
  }
}

async function backendStreamIsActive(): Promise<boolean | null> {
  const query = aiService.isStreaming;
  if (typeof query !== "function") return null;
  try {
    return await query();
  } catch {
    return null;
  }
}

async function waitForBackendStreamIdle(): Promise<boolean> {
  for (let attempt = 0; attempt < BUSY_RECONCILIATION_ATTEMPTS; attempt += 1) {
    const active = await backendStreamIsActive();
    if (active === null || !active) {
      if (active === false) aiState.globalStreamBusy = false;
      return active === false;
    }
    await new Promise<void>((resolve) => setTimeout(resolve, BUSY_RECONCILIATION_DELAY_MS));
  }
  return false;
}

/**
 * Reconcile the process-wide busy bit from the backend after a terminal
 * stream event. The terminal event can arrive before the worker has released
 * the backend slot, so poll briefly; never clear a busy bit based only on
 * local renderer state because another window may own the next stream.
 */
function reconcileGlobalStreamBusy(): void {
  const generation = ++busyReconciliationGeneration;
  const poll = async (): Promise<void> => {
    for (let attempt = 0; attempt < BUSY_RECONCILIATION_ATTEMPTS; attempt += 1) {
      if (generation !== busyReconciliationGeneration) return;
      const isStreaming = await backendStreamIsActive();
      if (generation !== busyReconciliationGeneration) return;
      if (isStreaming === null) return;
      if (!isStreaming) {
        aiState.globalStreamBusy = false;
        return;
      }
      await new Promise<void>((resolve) => setTimeout(resolve, BUSY_RECONCILIATION_DELAY_MS));
    }
  };
  void poll();
}

export function resetStreamState(): void {
  clearStreamTimeout();
  streamGeneration += 1;
  clearStreamAdmissionBuffer();
  if (pendingAssistantMessage && pendingAssistantMessage.content === "") {
    const idx = aiState.messages.indexOf(pendingAssistantMessage);
    if (idx >= 0) aiState.messages.splice(idx, 1);
  }
  pendingAssistantMessage = null;
  pendingAssistantSawNativeToolEvent = false;
  aiState.streaming = false;
  aiState.globalStreamBusy = false;
  busyReconciliationGeneration += 1;
  aiState.activeStreamId = null;
  aiState.conversationStaleWhileStreaming = false;
}

function startStreamTimeout(): void {
  clearStreamTimeout();
  streamTimeoutTimer = setTimeout(() => {
    streamTimeoutTimer = null;
    // 超时兜底：后端既未发 ai:done 也未发 ai:error，强制清理本地状态。
    if (aiState.streaming || pendingAssistantMessage) {
      const timedOutGeneration = streamGeneration;
      // Clearing renderer state alone leaves the provider worker alive and
      // can strand the process-wide stream slot. Request backend cancellation
      // before releasing the local ownership; late events are rejected by the
      // generation bump below. Stop failures remain visible in output rather
      // than silently pretending the provider was terminated.
      void Promise.resolve(aiService.stopStream()).catch((error: unknown) => {
        const stopError = errorMessage(error) || "failed to stop timed-out AI stream";
        pushOutput("ai", "error", `AI stream stop failed: ${stopError}`);
        if (streamGeneration === timedOutGeneration) {
          aiState.error = `${aiState.error ?? "AI stream timed out"}; ${stopError}`;
        }
      });
      const errMsg = "AI stream timed out";
      aiState.error = errMsg;
      if (pendingAssistantMessage && pendingAssistantMessage.content === "") {
        const idx = aiState.messages.indexOf(pendingAssistantMessage);
        if (idx >= 0) aiState.messages.splice(idx, 1);
      }
      pendingAssistantMessage = null;
      pendingAssistantSawNativeToolEvent = false;
      aiState.streaming = false;
      aiState.activeStreamId = null;
      aiState.globalStreamBusy = false;
      streamGeneration += 1;
      clearStreamAdmissionBuffer();
      // M-16: stream 异常中断时无条件重置 stale 标志。
      aiState.conversationStaleWhileStreaming = false;
      notifyError(errMsg, "AI Error");
      pushOutput("ai", "error", `AI error: ${errMsg}`);
    }
  }, STREAM_TIMEOUT_MS);
}

/**
 * prompt-6 Task 2: normalize AI stream event payloads.
 * Accepts legacy string/bool payloads and structured {streamId, data|busy}.
 */
export function parseAIStreamPayload(event: unknown): {
  streamId: string;
  data: string;
  busy?: boolean;
  raw: unknown;
} {
  const raw = unwrapEventData(event);
  if (typeof raw === "string") {
    return { streamId: "", data: raw, raw };
  }
  if (typeof raw === "boolean") {
    return { streamId: "", data: "", busy: raw, raw };
  }
  if (raw && typeof raw === "object") {
    const o = raw as Record<string, unknown>;
    const streamId = typeof o.streamId === "string" ? o.streamId : "";
    let data = "";
    if (typeof o.data === "string") data = o.data;
    else if (typeof o.message === "string") data = o.message;
    let busy: boolean | undefined;
    if (typeof o.busy === "boolean") busy = o.busy;
    else if (o.busy === "true") busy = true;
    else if (o.busy === "false") busy = false;
    return { streamId, data, busy, raw };
  }
  return { streamId: "", data: "", raw };
}

/** True if this window should apply the event for the given streamId. */
export function isOwnedStreamEvent(streamId: string): boolean {
  // Empty streamId = legacy: apply only if this window owns a pending stream.
  if (!streamId) {
    return !!pendingAssistantMessage || !!aiState.activeStreamId;
  }
  if (aiState.activeStreamId) {
    return aiState.activeStreamId === streamId;
  }
  // Race: chunks may arrive before StartStream's streamId is assigned.
  // If we have a pending assistant message, this window owns the active stream.
  return !!pendingAssistantMessage;
}

/** prompt-5 Task J: hard cap on retained chat messages (FIFO drop). */
export const MAX_AI_MESSAGES = 200;

function trimMessagesIfNeeded(): void {
  if (aiState.messages.length <= MAX_AI_MESSAGES) return;
  const drop = aiState.messages.length - MAX_AI_MESSAGES;
  aiState.messages.splice(0, drop);
}

/**
 * Enables AI event handling. Wails registration is owned by crossWindowSync;
 * handlers remain here because they depend on private stream state.
 */
export function ensureAIEventListeners(): void {
  if (eventListenersRegistered) return;
  eventListenersRegistered = true;
}

function canHandleAIEvent(): boolean {
  return eventListenersRegistered;
}

function applyAIChunk(data: string): void {
  if (pendingAssistantMessage) pendingAssistantMessage.content += data;
}

function applyAIDone(): void {
  clearStreamTimeout();
  if (!pendingAssistantMessage && !aiState.streaming) {
    aiState.activeStreamId = null;
    if (aiState.globalStreamBusy) reconcileGlobalStreamBusy();
    return;
  }
  const lastContent = pendingAssistantMessage?.content ?? "";
  const nativeToolEventSeen = pendingAssistantSawNativeToolEvent;
  if (
    pendingAssistantMessage &&
    lastContent === "" &&
    (pendingAssistantMessage.toolCalls?.length ?? 0) === 0
  ) {
    const idx = aiState.messages.indexOf(pendingAssistantMessage);
    if (idx >= 0) aiState.messages.splice(idx, 1);
  }
  pendingAssistantMessage = null;
  pendingAssistantSawNativeToolEvent = false;
  aiState.streaming = false;
  aiState.activeStreamId = null;
  reconcileGlobalStreamBusy();
  void persistConversation();
  if (agentState.mode === "agent" && lastContent) {
    onAssistantFinished(lastContent, nativeToolEventSeen);
  }
}

function applyAIError(data: string): void {
  clearStreamTimeout();
  if (!pendingAssistantMessage && !aiState.streaming) {
    aiState.activeStreamId = null;
    if (aiState.globalStreamBusy) reconcileGlobalStreamBusy();
    return;
  }
  const errMsg = data || "AI request failed";
  aiState.error = errMsg;
  notifyError(errMsg, "AI Error");
  pushOutput("ai", "error", `AI error: ${aiState.error}`);
  if (pendingAssistantMessage && pendingAssistantMessage.content === "") {
    const idx = aiState.messages.indexOf(pendingAssistantMessage);
    if (idx >= 0) aiState.messages.splice(idx, 1);
  }
  pendingAssistantMessage = null;
  pendingAssistantSawNativeToolEvent = false;
  aiState.streaming = false;
  aiState.activeStreamId = null;
  reconcileGlobalStreamBusy();
  aiState.conversationStaleWhileStreaming = false;
}

type ValidatedNativeToolCallPayload = NativeToolCallPayload & { id: string };

function isNativeToolCallPayload(value: unknown): value is ValidatedNativeToolCallPayload {
  if (!value || typeof value !== "object") return false;
  return "id" in value
    && typeof value.id === "string"
    && value.id.trim() !== ""
    && "name" in value
    && typeof value.name === "string"
    && value.name.trim() !== ""
    && "arguments" in value
    && typeof value.arguments === "string";
}

function applyAIToolCalls(data: string, raw: unknown): void {
  if (agentState.mode !== "agent") return;
  // Native events are authoritative for this assistant turn. Mark the turn
  // before validation so malformed native output cannot fall through and
  // execute a fenced block from the same provider response.
  pendingAssistantSawNativeToolEvent = true;
  let payload: unknown = data || raw;
  if (payload && typeof payload === "object" && "data" in payload) {
    payload = payload.data;
  }
  let decoded: unknown = payload;
  try {
    if (typeof payload === "string") decoded = JSON.parse(payload) as unknown;
  } catch {
    aiState.error = "Provider returned malformed native tool-call JSON";
    return;
  }
  if (!Array.isArray(decoded) || decoded.length === 0) {
    aiState.error = "Provider returned an empty native tool-call batch";
    return;
  }
  if (!decoded.every(isNativeToolCallPayload)) {
    aiState.error = "Provider returned an invalid native tool-call batch";
    return;
  }
  const parsed = decoded;
  const accepted = onNativeToolCalls(parsed);
  if (accepted < 0) {
    aiState.error = "Provider returned an invalid native tool-call batch";
    return;
  }
  if (pendingAssistantMessage) {
    const existing = new Map(
      (pendingAssistantMessage.toolCalls ?? []).map((call) => [call.id, call]),
    );
    for (const call of parsed) {
      existing.set(call.id, {
        id: call.id,
        name: call.name,
        arguments: call.arguments,
      });
    }
    pendingAssistantMessage.toolCalls = [...existing.values()];
  }
}

function replayAdmittedEvents(streamId: string, events: BufferedAIEvent[]): void {
  for (const event of events) {
    // A terminal event invalidates all later events in the same buffered
    // prefix, even when their stream ID matches.
    if (aiState.activeStreamId !== streamId || !aiState.streaming || !pendingAssistantMessage) break;
    switch (event.kind) {
      case "chunk":
        applyAIChunk(event.data);
        break;
      case "done":
        applyAIDone();
        break;
      case "error":
        applyAIError(event.data);
        break;
      case "tool_calls":
        applyAIToolCalls(event.data, event.raw);
        break;
      case "reasoning":
        if (event.data) recordProviderReasoningSummary(event.data);
        break;
    }
  }
}

export function handleAIChunkEvent(event: unknown): void {
  if (!canHandleAIEvent()) return;
  const payload = parseAIStreamPayload(event);
  if (bufferPreAdmissionEvent("chunk", payload)) return;
  const { streamId, data } = payload;
  if (!isOwnedStreamEvent(streamId)) return;
  if (typeof data === "string") applyAIChunk(data);
}

export function handleAIDoneEvent(event: unknown): void {
  if (!canHandleAIEvent()) return;
  const payload = parseAIStreamPayload(event);
  if (bufferPreAdmissionEvent("done", payload)) return;
  const { streamId } = payload;
  if (!isOwnedStreamEvent(streamId)) return;
  applyAIDone();
}

export function handleAIErrorEvent(event: unknown): void {
  if (!canHandleAIEvent()) return;
  const payload = parseAIStreamPayload(event);
  if (bufferPreAdmissionEvent("error", payload)) return;
  const { streamId, data } = payload;
  if (!isOwnedStreamEvent(streamId)) return;
  applyAIError(data);
}

export function handleAIStreamBusyEvent(event: unknown): void {
  if (!canHandleAIEvent()) return;
  const { busy, raw } = parseAIStreamPayload(event);
  if (typeof busy === "boolean") {
    aiState.globalStreamBusy = busy;
    return;
  }
  const val = Array.isArray(raw) ? raw[0] : raw;
  aiState.globalStreamBusy = val === true || val === "true";
}

export function handleAIToolCallsEvent(event: unknown): void {
  if (!canHandleAIEvent() || agentState.mode !== "agent") return;
  const payload = parseAIStreamPayload(event);
  // Tool calls and reasoning summaries can trigger side effects or timeline
  // entries; unlike legacy text chunks they must carry the backend authority
  // stream ID so a delayed event from a previous turn cannot be replayed.
  if (!payload.streamId) return;
  if (bufferPreAdmissionEvent("tool_calls", payload)) return;
  const { streamId, data, raw } = payload;
  if (!isOwnedStreamEvent(streamId)) return;
  applyAIToolCalls(data, raw);
}

/** Handles provider-declared summaries only; hidden reasoning is not inferred. */
export function handleAIReasoningEvent(event: unknown): void {
  if (!canHandleAIEvent()) return;
  const payload = parseAIStreamPayload(event);
  if (!payload.streamId) return;
  if (bufferPreAdmissionEvent("reasoning", payload)) return;
  const { streamId, data } = payload;
  if (!isOwnedStreamEvent(streamId)) return;
  if (data) recordProviderReasoningSummary(data);
}

export function handleConversationSavedEvent(event: unknown): void {
  if (!canHandleAIEvent()) return;
  const payload = unwrapEventData(event);
  const origin = parseSyncOrigin(payload);
  if (origin && origin === getWindowOriginId()) return;
  const id = payload && typeof payload === "object" && "id" in payload
    ? String((payload as { id?: unknown }).id ?? "")
    : "";
  if (!id) return;
  const eventRevision = payload && typeof payload === "object" && "revision" in payload
    ? Number((payload as { revision?: unknown }).revision)
    : NaN;
  if (isCurrentConversationLoadActive()) {
    // A peer can publish the terminal save while this renderer is still
    // loading the handoff snapshot. Remember that revision so loadConversation
    // can re-read before it commits and acknowledges stale content.
    if (activeConversationLoadID === id) {
      const revision = Number.isSafeInteger(eventRevision) && eventRevision > 0
        ? eventRevision
        : null;
      const previous = pendingConversationSavedRevisions.get(id);
      pendingConversationSavedRevisions.set(
        id,
        previous === null || revision === null
          ? null
          : Math.max(previous ?? 0, revision),
      );
    }
    // A load for another identity is authoritative. In particular, an event
    // for the previously visible conversation must not cancel that load.
    return;
  }
  if (aiState.currentConversationId !== id) return;
  // A late save from an earlier renderer snapshot must not reload a
  // conversation that has already been loaded at the same or newer revision.
  if (Number.isSafeInteger(eventRevision) && eventRevision <= aiState.conversationRevision) return;
  if (aiState.streaming || pendingAssistantMessage) {
    aiState.conversationStaleWhileStreaming = true;
    return;
  }
  void loadConversation(id);
}

/**
 * N-149: Cancels all AI event listeners. Intended for HMR teardown in dev
 * and test cleanup. After calling this, ensureAIEventListeners() can be
 * invoked again to re-register fresh listeners.
 *
 * N-NEW-1: 流式传输中调用 cleanup（HMR 热重载、测试 teardown）后，
 * ai:done / ai:error 永不再到达，UI 会卡在 streaming=true。cleanup 末尾
 * 必须重置流状态：streaming / globalStreamBusy / pendingAssistantMessage。
 */
export function cleanupAIEventListeners(): void {
  eventListenersRegistered = false;
  if (streamTimeoutTimer !== null) {
    clearTimeout(streamTimeoutTimer);
    streamTimeoutTimer = null;
  }
  resetStreamState();
}

import.meta.hot?.dispose(cleanupAIEventListeners);

/**
 * Builds the user message including context if attached.
 * Plan 11 Task 3: also serializes contextChips (file/symbol/codeblock/gitdiff/
 * web/url/docs) into the message prefix. Image chips are skipped here — vision
 * support requires backend changes and is handled separately.
 */
function needsCodebaseResolve(message: string): boolean {
  return /@codebase/i.test(message) || aiState.contextChips.some((chip) => chip.kind === "codebase");
}

export async function resolveCodebaseChips(message: string): Promise<void> {
  const chips = aiState.contextChips.filter((chip) => chip.kind === "codebase");
  const literal = message.match(/@codebase(?:\s+(.+))?/i);
  if (chips.length === 0 && literal) {
    aiState.contextChips.push({
      id: `codebase-${Date.now()}`,
      kind: "codebase",
      label: "Codebase",
      query: (literal[1] ?? message.replace(/@codebase/ig, "")).trim() || message.trim(),
    });
  }
  for (const chip of aiState.contextChips) {
    if (chip.kind !== "codebase") continue;
    const query = (chip.query ?? "").trim() || message.replace(/@codebase/ig, "").trim();
    chip.query = query;
    if (!query) {
      chip.content = "";
      continue;
    }
    try {
      const results = await searchService.search(appState.workspaceRoot || "", query, true);
      const lines: string[] = [];
      for (const file of results ?? []) {
        for (const match of file.matches ?? []) {
          lines.push(`${file.path}:${match.line}: ${match.preview}`);
          if (lines.length >= 10) break;
        }
        if (lines.length >= 10) break;
      }
      chip.content = lines.join("\n");
    } catch {
      chip.content = "";
    }
  }
}

function buildUserMessage(content: string): string {
  let prefix = "";
  if (aiState.mentionedFiles.length > 0) {
    prefix += "Referenced files:\n\n";
    for (const file of aiState.mentionedFiles) {
      prefix += `File: ${file.filePath}\n\`\`\`${file.language}\n${file.content}\n\`\`\`\n\n`;
    }
    prefix += "---\n\n";
  }
  // Plan 11 Task 3: serialize context chips.
  if (aiState.contextChips.length > 0) {
    for (const chip of aiState.contextChips) {
      switch (chip.kind) {
        case "file":
        case "symbol":
          if (chip.content) {
            prefix += `File: ${chip.filePath ?? chip.label}\n\`\`\`${chip.language ?? "text"}\n${chip.content}\n\`\`\`\n\n`;
          } else {
            prefix += `Reference: ${chip.label}\n\n`;
          }
          break;
        case "codeblock":
          prefix += `Code:\n\`\`\`${chip.language ?? "text"}\n${chip.content ?? ""}\n\`\`\`\n\n`;
          break;
        case "gitdiff":
          prefix += `Git diff:\n\`\`\`diff\n${chip.content ?? ""}\n\`\`\`\n\n`;
          break;
        case "web":
        case "url":
          if (chip.url) prefix += `Web reference: ${chip.url}\n\n`;
          break;
        case "docs":
          if (chip.query) prefix += `Docs query: ${chip.query}\n\n`;
          break;
        case "codebase":
          prefix += chip.content
            ? `Codebase text search (not a vector index) for ${chip.query ?? chip.label}:\n${chip.content}\n\n`
            : `Codebase text search (not a vector index) for ${chip.query ?? chip.label}: no matches.\n\n`;
          break;
        // Plan 11 Task 4: mcp chip 附加命名空间，提示 AI 可调用此工具。
        // P1-03-E: user-injected MCP resource/prompt context carries its
        // provenance header; content is untrusted context, never authority.
        case "mcp":
          if (chip.content) {
            prefix += `MCP context from ${chip.mcpServer ?? "unknown server"} (${chip.label}):\n${chip.content}\n\n`;
          } else {
            prefix += `MCP tool: ${chip.label}\n\n`;
          }
          break;
        // Plan 11 Task 5: skill chip 注入 SystemPrompt（G-SEC-03 已在前端确认）。
        case "skill":
          if (chip.content) {
            prefix += `[Active Skill: ${chip.label}]\n${chip.content}\n\n`;
          } else {
            prefix += `Active Skill: ${chip.label}\n\n`;
          }
          break;
        case "persona":
          prefix += `Persona: ${chip.label}\n\n`;
          break;
        // image: sent as structured image_url/image blocks via message.images
      }
    }
    prefix += "---\n\n";
  }
  if (aiState.context) {
    const ctx = aiState.context;
    if (ctx.kind === "selection") {
      prefix += `File: ${ctx.filePath}\nSelected code (${ctx.startLine}-${ctx.endLine}):\n\`\`\`${ctx.language}\n${ctx.content}\n\`\`\`\n\n`;
    } else {
      prefix += `File: ${ctx.filePath}\n\`\`\`${ctx.language}\n${ctx.content}\n\`\`\`\n\n`;
    }
  }
  return prefix + content;
}

/**
 * Sends a user message. Respects attached context and persists the conversation.
 * Uses event-based streaming (ai:chunk, ai:done, ai:error events from backend).
 */
async function sendMessageInternal(
  content: string,
  nativeToolResults?: NativeToolResultContext[],
): Promise<boolean> {
  if (aiState.streaming || sendAdmissionInFlight) return false;
  // prompt-5 Task B: another window owns the process-wide stream.
  if (aiState.globalStreamBusy) {
    notifyError(
      translate("aiChat.streamBusy"),
      translate("aiChat.streamBusyTitle"),
    );
    return false;
  }
  sendAdmissionInFlight = true;
  try {
    return await sendMessageInternalCore(content, nativeToolResults);
  } finally {
    sendAdmissionInFlight = false;
  }
}

async function sendMessageInternalCore(
  content: string,
  nativeToolResults?: NativeToolResultContext[],
): Promise<boolean> {
	if (agentState.mode === "agent") {
		try {
			await ensureAgentSession();
			await refreshAgentToolCatalog();
		} catch (error: unknown) {
			const message = `Agent tool catalog unavailable: ${errorMessage(error)}`;
			notifyError(message, "Agent Error");
			pushOutput("agent", "error", message);
			return false;
		}
	}
  ensureAIEventListeners();
  const generation = ++streamGeneration;

  aiState.error = null;
  const isNativeToolResultTurn = (nativeToolResults?.length ?? 0) > 0;
  if (!isNativeToolResultTurn && needsCodebaseResolve(content)) {
    await resolveCodebaseChips(content);
  }
  const fullContent = isNativeToolResultTurn ? content : buildUserMessage(content);
  // C-6: 生成稳定 id 替代数组索引作 v-for key，避免 FIFO drop 时 DOM 错位。
  // G12: image chips travel as structured attachments on this user message.
  const imageUrls = isNativeToolResultTurn ? [] : aiState.contextChips
    .filter((chip) => chip.kind === "image" && chip.imageUrl)
    .map((chip) => chip.imageUrl as string);
  const providerInputMessage = createMessage(
    isNativeToolResultTurn ? "tool" : "user",
    fullContent,
    imageUrls,
    undefined,
    nativeToolResults,
  );
  aiState.messages.push(providerInputMessage);
  if (!isNativeToolResultTurn) {
    clearMentionedFiles();
    clearContextChips();
  }
  aiState.streaming = true;
  // Optimistic busy until backend ai:stream-busy confirms (prompt-6 Task 5 allows optimistic UI).
  aiState.globalStreamBusy = true;

  const assistantDraft = createMessage("assistant", "");
  aiState.messages.push(assistantDraft);
  // Vue wraps objects stored in a reactive array when they are read back.
  // Keep the proxy as the streaming target; mutating the raw draft bypasses
  // dependency tracking and leaves the mounted chat stale until another
  // render or a conversation reload occurs.
  const assistantMessage = aiState.messages[aiState.messages.length - 1];
  if (!assistantMessage) {
    throw new Error("failed to create assistant message");
  }
  pendingAssistantMessage = assistantMessage;
  pendingAssistantSawNativeToolEvent = false;
  beginStreamAdmission(generation);
  trimMessagesIfNeeded();
  const rollbackUnadmittedMessages = (): void => {
    const messageIds = new Set([providerInputMessage.id, assistantMessage.id]);
    for (let index = aiState.messages.length - 1; index >= 0; index -= 1) {
      if (messageIds.has(aiState.messages[index]?.id ?? "")) {
        aiState.messages.splice(index, 1);
      }
    }
  };

  try {
    // In agent mode, use the agent system prompt instead of the user's
    // configured prompt. The agent prompt defines the tool-call protocol.
    // Project rules (#25) are appended to whichever prompt is in use so the
    // AI obeys project conventions even in agent mode.
    // N-60: In chat mode, if the conversation has a per-session system
    // prompt override, use it instead of the global appState.aiSystemPrompt.
    // N-59: When no custom prompt is set (neither override nor global),
    // fall back to the localized default from the i18n dictionary so zh/ja
    // users get a prompt in their language. The English i18n entry matches
    // the Go const in ai_prompts.go, so en users see the same prompt.
    let basePrompt: string;
    if (agentState.mode === "agent") {
      basePrompt = await getAgentSystemPrompt();
    } else if (aiState.currentSystemPromptOverride) {
      basePrompt = aiState.currentSystemPromptOverride;
    } else if (activePersona.value?.systemPrompt?.trim()) {
      // G12: an active persona defines the chat prompt and takes precedence
      // over the global prompt so the selection reaches the provider request
      // instead of being display-only.
      basePrompt = activePersona.value.systemPrompt.trim();
    } else if (appState.aiSystemPrompt) {
      basePrompt = appState.aiSystemPrompt;
    } else {
      // N-59: localized default system prompt
      basePrompt = translate("prompts.defaultSystem");
    }
    let systemPrompt = basePrompt + rulesForPrompt.value;
    // G12: an active Plan must influence the next request, not just the UI.
    // The goal and steps are serialized into the system prompt so the
    // provider sees the same plan the user is approving/executing.
    if (activePlan.value) {
      const plan = activePlan.value;
      const steps = plan.steps
        .map(
          (step, idx) =>
            `  ${idx + 1}. [${step.status}] ${step.title}${step.description ? `: ${step.description}` : ""}`,
        )
        .join("\n");
      systemPrompt += `\n\nActive plan (user-approved goal):\nGoal: ${plan.goal}\nSteps:\n${steps}\n`;
    }
    // Pass temperature and protocol from the active AI provider config so the
    // backend uses the right request shape (OpenAI vs Anthropic) and sampling.
    const activeCfg = activeAIConfig();
    await aiService.setConfig({
      // G-SEC-07: use the stored key (backend fetches from SettingsService).
      // The plaintext key never crosses the Wails binding.
      useStoredKey: true,
      configId: appState.activeAIConfigId,
      provider: activeCfg?.provider ?? appState.aiProvider,
      baseUrl: appState.aiBaseUrl,
      model: appState.aiModel,
      systemPrompt,
      // Plan 54: pass prompt overrides so the backend's GetEffective*
      // methods return the user-configured prompt instead of the built-in.
      agentSystemPrompt: appState.aiAgentSystemPrompt,
      conversationTitlePrompt: appState.aiConversationTitlePrompt,
      inlineCompletionPrompt: appState.aiInlineCompletionPrompt,
      temperature: appState.temperature,
      reasoningEffort: activeCfg?.reasoningEffort ?? appState.reasoningEffort,
      protocol: activeCfg?.protocol ?? "openai",
      maxTokens: appState.maxTokens,
      // prompt-5 Task H: native tools in agent mode (fence remains dual-track).
      tools: agentState.mode === "agent" ? buildNativeToolDefs() : [],
    });
    const history = aiState.messages.slice(0, -1);
		// Agent streams carry an explicit backend-owned persistent session. Chat
		// mode keeps the legacy per-request stream lifecycle.
		const streamStart = agentState.mode === "agent"
			&& typeof aiService.startAgentStream === "function"
			? await aiService.startAgentStream(agentState.sessionId, history)
			: await aiService.startStream(history);
		const streamId = typeof streamStart === "string" ? streamStart : streamStart.streamId;
		if (typeof streamStart !== "string" && streamStart.sessionId) {
			agentState.sessionId = streamStart.sessionId;
		}
		const admitted = takeStreamAdmission(generation, streamId);
    if (
      generation !== streamGeneration ||
      pendingAssistantMessage !== assistantMessage ||
      !aiState.streaming
    ) {
      clearStreamAdmissionBuffer(generation);
      rollbackUnadmittedMessages();
      return false;
    }
		if (typeof streamId !== "string" || !streamId.trim()) {
			throw new Error("AI stream returned an empty stream ID");
		}
		if (admitted?.poisoned) {
			clearStreamTimeout();
			rollbackUnadmittedMessages();
			pendingAssistantMessage = null;
			pendingAssistantSawNativeToolEvent = false;
			aiState.streaming = false;
			aiState.activeStreamId = null;
			aiState.globalStreamBusy = false;
			aiState.error = "AI stream event buffer exceeded safety limit";
			void aiService.stopStream().catch(() => undefined);
      return false;
		}
		aiState.activeStreamId = streamId;
    // M-16: 流已发起，启动超时兜底定时器。若后端在 STREAM_TIMEOUT_MS 内
    // 既未发 ai:done 也未发 ai:error，强制清理本地 streaming 状态。
    startStreamTimeout();
		if (admitted) replayAdmittedEvents(streamId, admitted.events);
    // Stream continues async; ai:done/ai:error events handle completion
    // A valid stream ID has been returned and assigned to this renderer.
    return true;
  } catch (e: unknown) {
    rollbackUnadmittedMessages();
    if (generation !== streamGeneration) return false;
    clearStreamAdmissionBuffer(generation);
    // M-16: 流从未发起，清除可能残留的超时定时器。
    clearStreamTimeout();
    const msg = errorMessage(e) || "AI request failed";
    aiState.error = msg;
    // Friendlier copy for dual-window mutual exclusion (backend ErrStreamBusy).
    if (/already in progress|stream is already/i.test(msg)) {
      notifyError(translate("aiChat.streamBusy"), translate("aiChat.streamBusyTitle"));
    } else {
      notifyError(msg, "AI Error");
    }
    pendingAssistantMessage = null;
    pendingAssistantSawNativeToolEvent = false;
    aiState.streaming = false;
    aiState.activeStreamId = null;
    // Only clear optimistic busy if backend never confirmed ownership.
    aiState.globalStreamBusy = false;
  }
    return false;
}

export async function sendMessage(content: string): Promise<boolean> {
  return sendMessageInternal(content);
}

export async function sendNativeToolResults(
  results: NativeToolResultContext[],
): Promise<boolean> {
  if (results.length === 0) throw new Error("Native tool results are required");
  const seen = new Set<string>();
  for (const result of results) {
    if (!result.toolCallId?.trim() || seen.has(result.toolCallId)) {
      throw new Error("Native tool results require unique provider call IDs");
    }
    seen.add(result.toolCallId);
  }
  const display = results.length === 1
    ? results[0].content
    : results.map((result) => `${result.toolCallId}: ${result.content}`).join("\n");
  for (let attempt = 0; attempt < 2; attempt += 1) {
    const sent = await sendMessageInternal(display, results);
    if (sent) return true;
    if (attempt === 0) {
      // A terminal event and the backend's busy=false publication can be
      // separated by a short worker cleanup window. Reconcile before retrying
      // so a lost event cannot silently drop the Agent's next turn.
      const idle = await waitForBackendStreamIdle();
      if (!idle) break;
    }
  }
  const message = "Agent tool result could not be submitted; the next provider turn was not started.";
  aiState.error = message;
  notifyError(message, "Agent Error");
  pushOutput("agent", "error", message);
  return false;
}

/**
 * Stops an in-progress streaming request.
 * prompt-6 Task 5: do not clear globalStreamBusy locally — StopStream on the
 * backend emits ai:stream-busy=false. Local streaming flag ends so UI unlocks.
 */
export async function stopGeneration(): Promise<boolean> {
  try {
    await aiService.stopStream();
  } catch (error: unknown) {
    const message = errorMessage(error) || "Failed to stop AI stream";
    aiState.error = message;
    notifyError(message, "AI Error");
    pushOutput("ai", "error", `AI stream stop failed: ${message}`);
    return false;
  }
  // M-16: 手动停止也清除超时兜底定时器。
  clearStreamTimeout();
  streamGeneration += 1;
  clearStreamAdmissionBuffer();
  aiState.streaming = false;
  aiState.activeStreamId = null;
  pendingAssistantMessage = null;
  pendingAssistantSawNativeToolEvent = false;
  // globalStreamBusy cleared by ai:stream-busy event from backend.
  return true;
}

/**
 * Attaches code context to the next message.
 */
export function attachContext(context: AIContextAttachment): void {
  aiState.context = context;
}

/**
 * Clears attached context.
 */
export function clearContext(): void {
  aiState.context = null;
}

/**
 * Adds a mentioned file to the AI context for the next message.
 */
export function addMentionedFile(entry: FileContextEntry): void {
  if (aiState.mentionedFiles.some(f => f.filePath === entry.filePath)) return;
  aiState.mentionedFiles.push(entry);
}

/**
 * Removes a mentioned file from the AI context by path.
 */
export function removeMentionedFile(filePath: string): void {
  const idx = aiState.mentionedFiles.findIndex(f => f.filePath === filePath);
  if (idx >= 0) aiState.mentionedFiles.splice(idx, 1);
}

/**
 * Clears all mentioned files.
 */
export function clearMentionedFiles(): void {
  aiState.mentionedFiles = [];
}

// --- Plan 11 Task 3: context chip management ---

/**
 * Adds a context chip for the next message. Chips are serialized by
 * buildUserMessage and cleared after send. Duplicate ids are ignored.
 */
export function addContextChip(chip: ContextChip): void {
  if (aiState.contextChips.some((c) => c.id === chip.id)) return;
  aiState.contextChips.push(chip);
}

/**
 * P1-03-E: inserts or replaces a chip by id. MCP resource/prompt injections
 * re-inject the same source with fresh content instead of duplicating chips.
 */
export function upsertContextChip(chip: ContextChip): void {
  const existing = aiState.contextChips.findIndex((c) => c.id === chip.id);
  if (existing >= 0) {
    aiState.contextChips.splice(existing, 1, chip);
    return;
  }
  aiState.contextChips.push(chip);
}

/**
 * Removes a context chip by id.
 */
export function removeContextChip(id: string): void {
  const idx = aiState.contextChips.findIndex((c) => c.id === id);
  if (idx >= 0) aiState.contextChips.splice(idx, 1);
}

/**
 * Clears all context chips.
 */
export function clearContextChips(): void {
  aiState.contextChips = [];
}

/**
 * Runs a preset AI action on the given code.
 * Fetches the instruction template from the backend (centralized prompt management).
 */
export async function runAIAction(
  action: AIActionName,
  code: string,
  language: string,
  filePath: string,
): Promise<void> {
  let instruction: string;
  try {
    instruction = await aiService.getPresetPrompt(action);
  } catch {
    instruction = action.replace(/_/g, " ");
  }
  const context: AIContextAttachment = {
    kind: "selection",
    filePath,
    language,
    content: code,
  };
  attachContext(context);
  await sendMessage(instruction);
  clearContext();
}

/**
 * Clears all messages and starts fresh.
 */
export function isConversationTransitionBlocked(): boolean {
  return aiState.streaming ||
    aiState.globalStreamBusy ||
    aiState.activeStreamId !== null ||
    pendingAssistantMessage !== null;
}

function reportBlockedConversationTransition(): void {
  const message = translate("aiChat.streamBusy");
  aiState.error = message;
  notifyError(message, translate("aiChat.streamBusyTitle"));
}

export function clearMessages(): boolean {
  if (isConversationTransitionBlocked()) {
    reportBlockedConversationTransition();
    return false;
  }
  // Invalidate a load that was started before the user chose a new
  // conversation. Without this guard, its late response can repopulate a
  // conversation that the user already cleared or replaced.
  conversationLoadGeneration += 1;
  aiState.messages = [];
  aiState.error = null;
  aiState.currentConversationId = null;
  aiState.currentConversationTitle = null;
  aiState.conversationRevision = 0;
  aiState.conversationStaleWhileStreaming = false;
  aiState.context = null;
  // N-60: Reset per-session system prompt override.
  aiState.currentSystemPromptOverride = null;
	clearPendingToolCalls();
  return true;
}

/**
 * Deletes a single message by index. If the conversation has only one message
 * left, it behaves like clearMessages. Persists the updated conversation.
 */
export async function deleteMessage(index: number): Promise<void> {
  if (isConversationTransitionBlocked()) return;
  if (index < 0 || index >= aiState.messages.length) return;
  aiState.messages.splice(index, 1);
  if (aiState.messages.length === 0) {
    clearMessages();
    return;
  }
  void persistConversation();
}

/**
 * Revokes (rolls back) messages from the given index to the end. This removes
 * the last N messages, useful for "undo last turn" or "regenerate from here".
 * Returns the revoked messages so the caller can optionally re-send.
 */
export async function revokeMessagesFrom(index: number): Promise<AIStoreMessage[]> {
  if (isConversationTransitionBlocked()) return [];
  if (index < 0 || index >= aiState.messages.length) return [];
  const revoked = aiState.messages.splice(index);
  if (aiState.messages.length === 0) {
    clearMessages();
  } else {
    void persistConversation();
  }
  return revoked;
}

/**
 * Persists the current conversation to disk (prompt-7 Task C CAS).
 */
interface ConversationPersistResult {
  id: string;
  title: string;
  revision: number;
}

async function persistConversation(): Promise<ConversationPersistResult | null> {
  if (aiState.messages.length === 0) return null;
  const snapshot = {
    generation: conversationLoadGeneration,
    conversationId: aiState.currentConversationId,
    title: aiState.currentConversationTitle,
    revision: aiState.conversationRevision,
    messages: aiState.messages.map((message) => ({
      role: message.role,
      content: message.content,
      toolCalls: message.toolCalls?.map((call) => ({ ...call })),
      toolResults: message.toolResults?.map((result) => ({ ...result })),
    })),
    systemPromptOverride: aiState.currentSystemPromptOverride,
    skipAITitle:
      aiState.streaming ||
      aiState.globalStreamBusy ||
      aiState.activeStreamId != null,
  };
  const task = conversationPersistTail.then(() => persistConversationSnapshot(snapshot));
  // Keep a non-rejecting tail so one backend failure cannot disable every
  // later save. The returned task still lets callers wait for this attempt.
  conversationPersistTail = task.then(() => undefined, () => undefined);
  return task;
}

/**
 * Forces the current renderer snapshot through the persistence queue before a
 * cross-window handoff. This is safe during a live stream: the partial turn
 * receives an ID now and the terminal save updates that same conversation.
 */
export async function persistConversationNow(): Promise<string | null> {
  const result = await persistConversation();
  await flushConversationPersistence();
  return result?.id ?? null;
}

/** Waits until all saves captured before this call have settled. */
export async function flushConversationPersistence(): Promise<void> {
  await conversationPersistTail;
}

interface ConversationPersistSnapshot {
  generation: number;
  conversationId: string | null;
  title: string | null;
  revision: number;
  messages: Array<{
    role: ChatMessage["role"];
    content: string;
    toolCalls?: NativeToolCallContext[];
    toolResults?: NativeToolResultContext[];
  }>;
  systemPromptOverride: string | null;
  skipAITitle: boolean;
}

function canPublishConversationSnapshot(snapshot: ConversationPersistSnapshot): boolean {
  return snapshot.generation === conversationLoadGeneration &&
    !isCurrentConversationLoadActive() &&
    aiState.currentConversationId === snapshot.conversationId;
}

async function persistConversationSnapshot(
  snapshot: ConversationPersistSnapshot,
): Promise<ConversationPersistResult | null> {
  try {
    // Multiple saves can be captured while the first new-conversation ID is
    // still being generated. Once the preceding queued save publishes that
    // identity, later snapshots from the same generation continue it instead
    // of forking a second backend record.
    const continuedNewConversation = !snapshot.conversationId &&
      snapshot.generation === conversationLoadGeneration &&
      !isCurrentConversationLoadActive() &&
      aiState.currentConversationId !== null;
    const wasNew = !snapshot.conversationId && !continuedNewConversation;
    let id = continuedNewConversation
      ? aiState.currentConversationId
      : snapshot.conversationId;
    const snapshotTitle = continuedNewConversation
      ? aiState.currentConversationTitle
      : snapshot.title;
    const snapshotRevision = continuedNewConversation
      ? aiState.conversationRevision
      : snapshot.revision;
    if (!id) {
      id = await conversationService.generateId();
    }
    // Generate a title only for new conversations; reuse the existing title
    // for subsequent persists to avoid redundant API calls and title churn.
    let title = snapshotTitle;
    if (wasNew || !title) {
      const firstUser = snapshot.messages.find((message) => message.role === "user");
      if (firstUser) {
        // prompt-7 Task E / BUG-M13: skip AI title when stream busy / streaming.
        try {
          if (snapshot.skipAITitle) {
            title = await conversationService.generateTitle(firstUser.content.slice(0, 200));
          } else {
            title = await aiService.generateTitleWithAI(firstUser.content.slice(0, 500));
          }
        } catch {
          title = await conversationService.generateTitle(firstUser.content.slice(0, 200));
        }
      } else {
        title = "(empty)";
      }
    }
    const baseRev = snapshotRevision;
    const payload: {
      id: string;
      title: string;
      created_at: number;
      messages: Array<{
        role: string;
        content: string;
        toolCalls?: NativeToolCallContext[];
        toolResults?: NativeToolResultContext[];
      }>;
      system_prompt_override?: string;
      expected_revision?: number;
    } = {
      id,
      title,
      created_at: Math.floor(Date.now() / 1000),
      messages: snapshot.messages,
      system_prompt_override: snapshot.systemPromptOverride ?? undefined,
    };
    // CAS only when we already know a disk revision (not brand-new id).
    if (!wasNew && baseRev > 0) {
      payload.expected_revision = baseRev;
    }
    try {
      await conversationService.save(payload);
      // Optimistic bump; next load will SSOT.
      // The revision belongs to the captured identity, not whatever the user
      // may have loaded while this request was awaiting the backend.
    } catch (saveErr: unknown) {
      const msg = errorMessage(saveErr) || String(saveErr);
      if (/revision conflict|conversation revision/i.test(msg)) {
        // Conflict: fork local messages into a new conversation id.
        notifyError(
          translate("aiChat.conversationConflict"),
          translate("aiChat.conversationConflictTitle"),
        );
        const forkedId = await conversationService.generateId();
        const forkTitle = `${title} (${translate("aiChat.conversationForkSuffix")})`;
        await conversationService.save({
          id: forkedId,
          title: forkTitle,
          created_at: Math.floor(Date.now() / 1000),
          messages: snapshot.messages,
          system_prompt_override: snapshot.systemPromptOverride ?? undefined,
        });
        id = forkedId;
        title = forkTitle;
      } else {
        throw saveErr;
      }
    }
    const savedRevision = id === snapshot.conversationId && baseRev > 0
      ? baseRev + 1
      : 1;
    const canPublish = continuedNewConversation
      ? snapshot.generation === conversationLoadGeneration &&
        !isCurrentConversationLoadActive() &&
        aiState.currentConversationId === id
      : canPublishConversationSnapshot(snapshot);
    if (canPublish) {
      aiState.currentConversationId = id;
      aiState.currentConversationTitle = title;
      aiState.conversationRevision = savedRevision;
    }
    // prompt-6 Task 1: notify peer webviews to refresh conversation list / reload.
    try {
      void Events.Emit("conversation:saved", {
        origin: getWindowOriginId(),
        id,
        title,
        revision: savedRevision,
        at: Date.now(),
      });
    } catch {
      // Events may be unavailable in unit tests.
    }
    return { id, title, revision: savedRevision };
  } catch (e) {
    console.error("Failed to persist conversation:", e);
    return null;
  }
}

/**
 * Loads a saved conversation into the chat.
 */
export async function loadConversation(id: string): Promise<boolean> {
  if (isConversationTransitionBlocked()) {
    reportBlockedConversationTransition();
    return false;
  }
  const generation = ++conversationLoadGeneration;
  activeConversationLoadGeneration = generation;
  activeConversationLoadID = id;
  try {
    let conv: Awaited<ReturnType<typeof conversationService.load>> | null = null;
    let minimumRevision = 0;
    let forceReload = false;
    const maxAttempts = 3;
    for (let attempt = 0; attempt < maxAttempts; attempt += 1) {
      conv = await conversationService.load(id);
      if (generation !== conversationLoadGeneration) return false;

      if (pendingConversationSavedRevisions.has(id)) {
        const pendingRevision = pendingConversationSavedRevisions.get(id) ?? null;
        pendingConversationSavedRevisions.delete(id);
        if (pendingRevision === null) {
          forceReload = true;
        } else {
          minimumRevision = Math.max(minimumRevision, pendingRevision);
        }
      }
      const loadedRevision = Number(conv.revision ?? 0);
      const needsReload = forceReload || (
        minimumRevision > 0 &&
        (!Number.isSafeInteger(loadedRevision) || loadedRevision < minimumRevision)
      );
      if (!needsReload) break;
      if (attempt === maxAttempts - 1) {
        aiState.error = "Conversation changed while it was loading";
        return false;
      }
      forceReload = false;
    }
    if (!conv) return false;
		clearPendingToolCalls();
    // C-6: 为加载的持久化消息生成稳定 id，保证 v-for key 稳定。
    aiState.messages = restoreInterruptedNativeToolRound(
      conv.messages.map((m) =>
        createMessage(
          m.role as ChatMessage["role"],
          m.content,
          undefined,
          m.toolCalls?.map((call) => ({ ...call })),
          m.toolResults?.map((result) => ({ ...result })),
        ),
      ),
    );
    restoreAgentTimelineFromMessages(aiState.messages);
    aiState.currentConversationId = conv.id;
    aiState.currentConversationTitle = conv.title;
    aiState.conversationRevision = conv.revision ?? 0;
    aiState.conversationStaleWhileStreaming = false;
    // N-60: Restore the per-conversation system prompt override.
    aiState.currentSystemPromptOverride = conv.system_prompt_override ?? null;
    aiState.error = null;
    return true;
  } catch (e: unknown) {
    if (generation !== conversationLoadGeneration) return false;
    aiState.error = errorMessage(e) || "Failed to load conversation";
    return false;
  } finally {
    if (activeConversationLoadGeneration === generation) {
      activeConversationLoadGeneration = null;
      activeConversationLoadID = null;
    }
  }
}

/**
 * Rename the current conversation. Updates the backend.
 * Returns true on success, false on failure.
 */
export async function renameConversation(id: string, newTitle: string): Promise<boolean> {
  const trimmed = newTitle.trim();
  if (!trimmed) return false;
  try {
    await conversationService.updateTitle(id, trimmed);
    aiState.currentConversationTitle = trimmed;
    return true;
  } catch (e) {
    notifyError(`Failed to rename conversation: ${e instanceof Error ? e.message : String(e)}`);
    return false;
  }
}

/**
 * N-60: Sets or clears the per-conversation system prompt override.
 * When set to a non-empty string, subsequent sendMessage calls in chat
 * mode will use this prompt instead of the global appState.aiSystemPrompt.
 * Pass null or empty string to reset to the global default.
 * The override is persisted with the conversation on the next save.
 */
export function setSystemPromptOverride(prompt: string | null): void {
  const trimmed = prompt?.trim() ?? "";
  aiState.currentSystemPromptOverride = trimmed === "" ? null : trimmed;
}
