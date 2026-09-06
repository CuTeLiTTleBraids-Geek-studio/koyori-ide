// Koyori IDE 模块 · Ai Assistant；交互服务：窗口（WindowService）。
// 喵，这是 Koyori IDE 的 Ai Assistant 模块（前端实现）~
import { reactive } from "vue";
import { Events } from "@wailsio/runtime";
import { aiState, flushConversationPersistence, persistConversationNow } from "@/stores/ai";
import { setMode as setAgentMode } from "@/stores/agent";
import { windowService } from "@/api/services";
import { getWindowOriginId } from "@/lib/windowOrigin";

/**
 * Plan 11 Task 1 — AI 助手独立页面状态。
 *
 * `aiState`（@/stores/ai）管理对话内容（消息流/当前会话 ID），嵌入式
 * AiChatPanel 与独立页面 AiAssistantView 共享同一 `aiState` 实例，切换
 * 模式不丢失会话。本 store 只管独立页面的 UI 状态：当前模式、侧栏宽度、
 * 上下文面板折叠态、活动会话 ID 同步。
 *
 * 模式语义：
 *   - chat: 普通问答
 *   - goal: 目标驱动自治（Task 10）
 *   - agent: 工具调用代理（既有 Agent mode）
 */

export type AiMode = "chat" | "plan" | "goal" | "agent";
export type AISettingsSection = "ai" | "agent" | "prompts" | "presets" | "computerUse";

export interface AIConversationTarget {
  protocol: 1;
  target: "ai-window";
  conversationId: string | null;
  mode: AiMode;
  requestId: string;
  sourceOrigin: string;
  sourceEpoch: string;
  recipientEpoch: string | null;
  sequence: number;
  createdAt: number;
}

export interface AIConversationTargetAck {
  protocol: 1;
  target: "main-window";
  requestId: string;
  sourceOrigin: string;
  sourceEpoch: string;
  receiverEpoch: string;
  sequence: number;
  createdAt: number;
}

const AI_SETTINGS_TARGET_KEY = "koyori-ide.aiSettingsTarget";
const AI_SETTINGS_TARGET_TTL_MS = 2 * 60 * 1000;
export const AI_CONVERSATION_TARGET_KEY = "koyori-ide.aiConversationTarget";
export const AI_CONVERSATION_TARGET_ACK_KEY = "koyori-ide.aiConversationTargetAck";
const AI_CONVERSATION_RECEIVER_KEY = "koyori-ide.aiConversationReceiver";
const AI_CONVERSATION_TARGET_TTL_MS = 2 * 60 * 1000;
const AI_CONVERSATION_MAX_SEQUENCE = 1_000_000;
const AI_CONVERSATION_SEQUENCE_PER_EPOCH = 1024;
const AI_CONVERSATION_RECEIVER_TTL_MS = 5 * 60 * 1000;
let conversationTargetSequence = 0;
let conversationSourceEpoch = createProtocolId("source");
let activeDeliveryRequestId: string | null = null;
let conversationOpenInvocation = 0;
const observedConversationTargetAcks = new Map<string, AIConversationTargetAck>();
const conversationTargetAckWaiters = new Map<string, Set<() => void>>();

function createProtocolId(prefix: string): string {
  const random = typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
    ? crypto.randomUUID()
    : `${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 12)}`;
  return `${prefix}_${random}`;
}

function isProtocolId(value: unknown): value is string {
  return typeof value === "string" && value.length >= 8 && value.length <= 160 &&
    /^[A-Za-z0-9_.:-]+$/.test(value);
}

function isAiMode(value: unknown): value is AiMode {
  return value === "chat" || value === "plan" || value === "goal" || value === "agent";
}

function normalizeConversationTarget(
  value: unknown,
  expectedReceiverEpoch?: string,
): AIConversationTarget | undefined {
  if (!value || typeof value !== "object") return undefined;
  const candidate = value as Partial<AIConversationTarget>;
  const conversationId = candidate.conversationId;
  const sequence = candidate.sequence;
  if (conversationId !== null && (
    typeof conversationId !== "string" ||
    conversationId.trim() === "" ||
    conversationId.length > 512
  )) return undefined;
  if (!isAiMode(candidate.mode)) return undefined;
  if (
    candidate.protocol !== 1 ||
    candidate.target !== "ai-window" ||
    !isProtocolId(candidate.requestId) ||
    !isProtocolId(candidate.sourceOrigin) ||
    !isProtocolId(candidate.sourceEpoch) ||
    (candidate.recipientEpoch !== null && !isProtocolId(candidate.recipientEpoch))
  ) return undefined;
  if (
    expectedReceiverEpoch &&
    candidate.recipientEpoch !== null &&
    candidate.recipientEpoch !== expectedReceiverEpoch
  ) return undefined;
  if (
    typeof sequence !== "number" ||
    !Number.isSafeInteger(sequence) ||
    sequence <= 0 ||
    sequence > AI_CONVERSATION_MAX_SEQUENCE
  ) {
    return undefined;
  }
  if (typeof candidate.createdAt !== "number" || !Number.isFinite(candidate.createdAt)) return undefined;
  const age = Date.now() - candidate.createdAt;
  if (age > AI_CONVERSATION_TARGET_TTL_MS || age < -30_000) return undefined;
  return {
    protocol: 1,
    target: "ai-window",
    conversationId: conversationId === null ? null : conversationId.trim(),
    mode: candidate.mode,
    requestId: candidate.requestId,
    sourceOrigin: candidate.sourceOrigin,
    sourceEpoch: candidate.sourceEpoch,
    recipientEpoch: candidate.recipientEpoch,
    sequence,
    createdAt: candidate.createdAt,
  };
}

function normalizeConversationTargetAck(value: unknown): AIConversationTargetAck | undefined {
  if (!value || typeof value !== "object") return undefined;
  const candidate = value as Partial<AIConversationTargetAck>;
  if (
    candidate.protocol !== 1 ||
    candidate.target !== "main-window" ||
    !isProtocolId(candidate.requestId) ||
    !isProtocolId(candidate.sourceOrigin) ||
    !isProtocolId(candidate.sourceEpoch) ||
    !isProtocolId(candidate.receiverEpoch) ||
    typeof candidate.sequence !== "number" ||
    !Number.isSafeInteger(candidate.sequence) ||
    candidate.sequence <= 0 ||
    candidate.sequence > AI_CONVERSATION_MAX_SEQUENCE ||
    typeof candidate.createdAt !== "number" ||
    !Number.isFinite(candidate.createdAt)
  ) return undefined;
  const age = Date.now() - candidate.createdAt;
  if (age > AI_CONVERSATION_TARGET_TTL_MS || age < -30_000) return undefined;
  return {
    protocol: 1,
    target: "main-window",
    requestId: candidate.requestId,
    sourceOrigin: candidate.sourceOrigin,
    sourceEpoch: candidate.sourceEpoch,
    receiverEpoch: candidate.receiverEpoch,
    sequence: candidate.sequence,
    createdAt: candidate.createdAt,
  };
}

export function handleAIConversationTargetAckEvent(event: unknown): void {
  const raw = (event as { data?: unknown } | null)?.data;
  const payload = Array.isArray(raw) ? raw[0] : raw;
  const ack = normalizeConversationTargetAck(payload);
  if (!ack) return;
  observedConversationTargetAcks.set(ack.requestId, ack);
  if (observedConversationTargetAcks.size > 256) {
    const oldest = observedConversationTargetAcks.keys().next().value;
    if (typeof oldest === "string") observedConversationTargetAcks.delete(oldest);
  }
  for (const resolve of conversationTargetAckWaiters.get(ack.requestId) ?? []) resolve();
  conversationTargetAckWaiters.delete(ack.requestId);
}

function readAIConversationReceiverEpoch(): string | null {
  try {
    const raw = globalThis.localStorage?.getItem(AI_CONVERSATION_RECEIVER_KEY);
    if (!raw) return null;
    const candidate = JSON.parse(raw) as {
      target?: unknown;
      receiverEpoch?: unknown;
      createdAt?: unknown;
    };
    if (
      candidate.target !== "ai-window" ||
      !isProtocolId(candidate.receiverEpoch) ||
      typeof candidate.createdAt !== "number" ||
      Date.now() - candidate.createdAt > AI_CONVERSATION_RECEIVER_TTL_MS ||
      candidate.createdAt - Date.now() > 30_000
    ) return null;
    return candidate.receiverEpoch;
  } catch {
    return null;
  }
}

export function registerAIConversationTargetReceiver(): string {
  const receiverEpoch = createProtocolId("receiver");
  try {
    globalThis.localStorage?.setItem(AI_CONVERSATION_RECEIVER_KEY, JSON.stringify({
      target: "ai-window",
      receiverEpoch,
      createdAt: Date.now(),
    }));
  } catch {
    // Live Wails delivery can still reach the receiver when storage is unavailable.
  }
  return receiverEpoch;
}

export function unregisterAIConversationTargetReceiver(receiverEpoch: string): void {
  try {
    if (readAIConversationReceiverEpoch() === receiverEpoch) {
      globalThis.localStorage?.removeItem(AI_CONVERSATION_RECEIVER_KEY);
    }
  } catch {
    // A later receiver registration overwrites stale descriptors.
  }
}

interface AIConversationTargetSnapshot {
  conversationId: string | null;
  mode: AiMode;
}

interface StagedAIConversationTarget {
  target: AIConversationTarget;
  durable: boolean;
}

function storeAIConversationTarget(target: AIConversationTarget): boolean {
  try {
    const storage = globalThis.localStorage;
    if (!storage) return false;
    storage.setItem(AI_CONVERSATION_TARGET_KEY, JSON.stringify(target));
    return true;
  } catch {
    return false;
  }
}

function stageAIConversationTarget(snapshot?: AIConversationTargetSnapshot): StagedAIConversationTarget {
  conversationTargetSequence += 1;
  if (conversationTargetSequence > AI_CONVERSATION_SEQUENCE_PER_EPOCH) {
    conversationSourceEpoch = createProtocolId("source");
    conversationTargetSequence = 1;
  }
  const target: AIConversationTarget = {
    protocol: 1,
    target: "ai-window",
    conversationId: snapshot?.conversationId ?? (aiState.currentConversationId?.trim() || null),
    mode: snapshot?.mode ?? aiAssistantState.mode,
    requestId: createProtocolId("request"),
    sourceOrigin: getWindowOriginId(),
    sourceEpoch: conversationSourceEpoch,
    recipientEpoch: readAIConversationReceiverEpoch(),
    sequence: conversationTargetSequence,
    createdAt: Date.now(),
  };
  return { target, durable: storeAIConversationTarget(target) };
}

export function consumePendingAIConversationTarget(
  expectedSequence?: number,
  expectedReceiverEpoch?: string,
): AIConversationTarget | undefined {
  try {
    const raw = globalThis.localStorage?.getItem(AI_CONVERSATION_TARGET_KEY);
    if (!raw) return undefined;
    const target = normalizeConversationTarget(JSON.parse(raw), expectedReceiverEpoch);
    if (!target) {
      globalThis.localStorage?.removeItem(AI_CONVERSATION_TARGET_KEY);
      return undefined;
    }
    if (expectedSequence !== undefined && target.sequence !== expectedSequence) return undefined;
    globalThis.localStorage?.removeItem(AI_CONVERSATION_TARGET_KEY);
    return target;
  } catch {
    try {
      globalThis.localStorage?.removeItem(AI_CONVERSATION_TARGET_KEY);
    } catch {
      // Storage may itself be unavailable; there is nothing else to clear.
    }
    return undefined;
  }
}

export function readPendingAIConversationTarget(
  expectedReceiverEpoch?: string,
): AIConversationTarget | undefined {
  try {
    const raw = globalThis.localStorage?.getItem(AI_CONVERSATION_TARGET_KEY);
    if (!raw) return undefined;
    const target = normalizeConversationTarget(JSON.parse(raw), expectedReceiverEpoch);
    if (!target) globalThis.localStorage?.removeItem(AI_CONVERSATION_TARGET_KEY);
    return target;
  } catch {
    try {
      globalThis.localStorage?.removeItem(AI_CONVERSATION_TARGET_KEY);
    } catch {
      // Storage may itself be unavailable.
    }
    return undefined;
  }
}

export function parseAIConversationTargetEvent(
  event: unknown,
  expectedReceiverEpoch?: string,
): AIConversationTarget | undefined {
  const raw = (event as { data?: unknown } | null)?.data;
  const payload = Array.isArray(raw) ? raw[0] : raw;
  return normalizeConversationTarget(payload, expectedReceiverEpoch);
}

function isExactStoredTarget(
  target: AIConversationTarget,
  receiverEpoch?: string,
): boolean {
  const stored = readPendingAIConversationTarget(receiverEpoch);
  return stored?.requestId === target.requestId &&
    stored.conversationId === target.conversationId &&
    stored.mode === target.mode &&
    stored.sourceOrigin === target.sourceOrigin &&
    stored.sourceEpoch === target.sourceEpoch &&
    stored.recipientEpoch === target.recipientEpoch &&
    stored.sequence === target.sequence &&
    stored.createdAt === target.createdAt;
}

export function acknowledgeAIConversationTarget(
  target: AIConversationTarget,
  receiverEpoch: string,
): void {
  if (
    target.recipientEpoch !== null &&
    target.recipientEpoch !== receiverEpoch
  ) return;
  const ack: AIConversationTargetAck = {
    protocol: 1,
    target: "main-window",
    requestId: target.requestId,
    sourceOrigin: target.sourceOrigin,
    sourceEpoch: target.sourceEpoch,
    receiverEpoch,
    sequence: target.sequence,
    createdAt: Date.now(),
  };
  try {
    globalThis.localStorage?.setItem(AI_CONVERSATION_TARGET_ACK_KEY, JSON.stringify(ack));
    if (isExactStoredTarget(target, receiverEpoch)) {
      globalThis.localStorage?.removeItem(AI_CONVERSATION_TARGET_KEY);
    }
  } catch {
    // The event acknowledgement remains available if localStorage fails.
  }
  try {
    void Promise.resolve(Events.Emit("ai:open-conversation-ack", ack)).catch(() => undefined);
  } catch {
    // The durable acknowledgement remains observable to the sender.
  }
}

function matchesAIConversationTargetAck(
  target: AIConversationTarget,
  ack: Partial<AIConversationTargetAck>,
): boolean {
  return ack.protocol === 1 &&
    ack.target === "main-window" &&
    ack.requestId === target.requestId &&
    ack.sourceOrigin === target.sourceOrigin &&
    ack.sourceEpoch === target.sourceEpoch &&
    ack.sequence === target.sequence &&
    isProtocolId(ack.receiverEpoch) &&
    (target.recipientEpoch === null || ack.receiverEpoch === target.recipientEpoch) &&
    typeof ack.createdAt === "number" &&
    Number.isFinite(ack.createdAt) &&
    Date.now() - ack.createdAt <= AI_CONVERSATION_TARGET_TTL_MS &&
    ack.createdAt - Date.now() <= 30_000;
}

function findAIConversationTargetAck(
  target: AIConversationTarget,
): AIConversationTargetAck | undefined {
  const observed = observedConversationTargetAcks.get(target.requestId);
  if (observed && matchesAIConversationTargetAck(target, observed)) return observed;
  try {
    const raw = globalThis.localStorage?.getItem(AI_CONVERSATION_TARGET_ACK_KEY);
    if (!raw) return undefined;
    const ack = JSON.parse(raw) as Partial<AIConversationTargetAck>;
    return matchesAIConversationTargetAck(target, ack)
      ? normalizeConversationTargetAck(ack)
      : undefined;
  } catch {
    return undefined;
  }
}

function hasAIConversationTargetAck(target: AIConversationTarget): boolean {
  return findAIConversationTargetAck(target) !== undefined;
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => globalThis.setTimeout(resolve, ms));
}

async function waitForConversationTargetAck(
  target: AIConversationTarget,
  timeoutMs: number,
): Promise<void> {
  if (hasAIConversationTargetAck(target)) return;
  let waiter: (() => void) | undefined;
  const acknowledged = new Promise<void>((resolve) => {
    waiter = resolve;
    const waiters = conversationTargetAckWaiters.get(target.requestId) ?? new Set<() => void>();
    waiters.add(resolve);
    conversationTargetAckWaiters.set(target.requestId, waiters);
  });
  await Promise.race([acknowledged, delay(timeoutMs)]);
  if (waiter) {
    const waiters = conversationTargetAckWaiters.get(target.requestId);
    waiters?.delete(waiter);
    if (waiters?.size === 0) conversationTargetAckWaiters.delete(target.requestId);
  }
}

interface AIConversationTargetDelivery {
  acknowledged: boolean;
  durable: boolean;
  target: AIConversationTarget;
  ack?: AIConversationTargetAck;
}

async function deliverAIConversationTarget(
  initialTarget: AIConversationTarget,
  initiallyDurable: boolean,
): Promise<AIConversationTargetDelivery> {
  let target = initialTarget;
  let durable = initiallyDurable && isExactStoredTarget(
    initialTarget,
    initialTarget.recipientEpoch ?? undefined,
  );
  activeDeliveryRequestId = target.requestId;
  for (const retryDelay of [0, 50, 150, 300, 500]) {
    if (activeDeliveryRequestId !== target.requestId) {
      return { acknowledged: false, durable, target };
    }
    const existingAck = findAIConversationTargetAck(target);
    if (existingAck) return { acknowledged: true, durable, target, ack: existingAck };
    if (retryDelay > 0) await waitForConversationTargetAck(target, retryDelay);
    if (activeDeliveryRequestId !== target.requestId) {
      return { acknowledged: false, durable, target };
    }
    const waitedAck = findAIConversationTargetAck(target);
    if (waitedAck) return { acknowledged: true, durable, target, ack: waitedAck };
    const currentReceiverEpoch = readAIConversationReceiverEpoch();
    if (currentReceiverEpoch !== target.recipientEpoch) {
      target = { ...target, recipientEpoch: currentReceiverEpoch };
    }
    storeAIConversationTarget(target);
    // Durability belongs to this exact receiver-bound target. A successful
    // write for an earlier receiver epoch cannot be carried forward after the
    // companion WebView restarts.
    durable = isExactStoredTarget(target, target.recipientEpoch ?? undefined);
    try {
      await Promise.resolve(Events.Emit("ai:open-conversation", target));
    } catch {
      // Retry while preserving the durable target for startup consumption.
    }
  }
  await waitForConversationTargetAck(target, 150);
  const ack = activeDeliveryRequestId === target.requestId
    ? findAIConversationTargetAck(target)
    : undefined;
  return {
    acknowledged: ack !== undefined,
    durable,
    target,
    ack,
  };
}

function isAISettingsSection(value: unknown): value is AISettingsSection {
  return value === "ai" || value === "agent" || value === "prompts" ||
    value === "presets" || value === "computerUse";
}

function persistAISettingsSection(section: AISettingsSection): void {
  try {
    globalThis.localStorage?.setItem(AI_SETTINGS_TARGET_KEY, JSON.stringify({
      section,
      createdAt: Date.now(),
    }));
  } catch {
    // The Wails event below still handles an already-open AI window.
  }
}

export function consumePendingAISettingsSection(): AISettingsSection | undefined {
  try {
    const raw = globalThis.localStorage?.getItem(AI_SETTINGS_TARGET_KEY);
    if (!raw) return undefined;
    globalThis.localStorage?.removeItem(AI_SETTINGS_TARGET_KEY);
    const pending = JSON.parse(raw) as { section?: unknown; createdAt?: unknown };
    if (!isAISettingsSection(pending.section) || typeof pending.createdAt !== "number") {
      return undefined;
    }
    if (Date.now() - pending.createdAt > AI_SETTINGS_TARGET_TTL_MS) return undefined;
    return pending.section;
  } catch {
    return undefined;
  }
}

export function parseAISettingsSectionEvent(event: unknown): AISettingsSection | undefined {
  const raw = (event as { data?: unknown } | null)?.data;
  const payload = Array.isArray(raw) ? raw[0] : raw;
  if (!payload || typeof payload !== "object") return undefined;
  const section = (payload as { section?: unknown }).section;
  return isAISettingsSection(section) ? section : undefined;
}

export interface AiAssistantState {
  /** 当前交互模式，决定右侧面板与工具集。 */
  mode: AiMode;
  /** 左侧会话列表宽度（px），可拖拽调整。 */
  sidebarWidth: number;
  /** 右侧上下文面板是否折叠。 */
  contextPanelCollapsed: boolean;
  /** 独立页面当前活动会话 ID，与 aiState.currentConversationId 同步。 */
  activeConversationId: string | null;
}

export interface AIConversationHandoffReceipt {
  requestId: string;
  sourceOrigin: string;
  sourceEpoch: string;
  sequence: number;
  recipientEpoch: string | null;
  receiverEpoch: string | null;
  acknowledged: boolean;
  durable: boolean;
}

export const aiAssistantState = reactive<AiAssistantState>({
  mode: "chat",
  sidebarWidth: 260,
  contextPanelCollapsed: false,
  activeConversationId: null,
});

/**
 * 打开 AI 助手独立页面 /ai。先将当前会话 ID 同步到独立页面状态，再通过
 * hash 路由导航。嵌入式 AiChatPanel 与独立页面共享 `aiState`，切换不丢失
 * 会话内容。
 */
export function openStandalonePage(): void {
  aiAssistantState.activeConversationId = aiState.currentConversationId;
  if (window.location.hash !== "#/ai") {
    window.location.hash = "#/ai";
  }
}

/**
 * prompt-4: 打开（或聚焦）OS 级 AI 伴侣窗口。
 * 与 /ai 路由页面并存：本函数创建第二个 WebviewWindow，不切换主窗口路由。
 */
export async function openAIDesktopWindow(
  section?: AISettingsSection,
  options: { requireConversationAck?: boolean } = {},
): Promise<AIConversationHandoffReceipt | null> {
  const invocation = ++conversationOpenInvocation;
  // Capture the user-visible identity synchronously. Persistence and native
  // window creation both await external work, so reading these fields later
  // can hand a different conversation/mode to the companion window.
  const invocationConversationId = aiState.currentConversationId?.trim() || null;
  const invocationMode = aiAssistantState.mode;
  const hasConversationContent = aiState.messages.length > 0;
  let durableConversationId = invocationConversationId;
  if (hasConversationContent) {
    // Persist the exact snapshot captured by persistConversationNow before a
    // second WebView loads it. This also assigns the first durable ID while a
    // stream is still active and returns that ID even if the main view changes
    // conversation while the save is in flight.
    durableConversationId = await persistConversationNow();
    if (!durableConversationId) {
      throw new Error("Failed to persist the AI conversation before opening its window");
    }
  } else {
    await flushConversationPersistence();
  }
  // Persistence can finish out of order across rapid clicks. Only the newest
  // user intent may open/publish a target; an older completion must not take
  // the companion window back to a stale conversation.
  if (invocation !== conversationOpenInvocation) {
    if (options.requireConversationAck) {
      throw new Error("AI conversation handoff was superseded by a newer request");
    }
    return null;
  }
  const snapshot: AIConversationTargetSnapshot = {
    conversationId: durableConversationId,
    mode: invocationMode,
  };
  aiAssistantState.activeConversationId = snapshot.conversationId;
  if (section) persistAISettingsSection(section);
  await windowService.openAIWindow();
  if (invocation !== conversationOpenInvocation) {
    if (options.requireConversationAck) {
      throw new Error("AI conversation handoff was superseded by a newer request");
    }
    return null;
  }
  const staged = stageAIConversationTarget(snapshot);
  const delivery = deliverAIConversationTarget(staged.target, staged.durable);
  let outcome: AIConversationTargetDelivery | undefined;
  // A receiver-bound target can become stale while the companion window is
  // opening. Observe the bounded delivery result so an epoch rollover plus a
  // storage/event failure is reported instead of silently losing the handoff.
  if (
    options.requireConversationAck ||
    !staged.durable ||
    staged.target.recipientEpoch !== null
  ) {
    outcome = await delivery;
    if (options.requireConversationAck && !outcome.acknowledged) {
      throw new Error("AI companion window did not acknowledge the conversation handoff");
    }
    if (!outcome.acknowledged && !outcome.durable && invocation === conversationOpenInvocation) {
      throw new Error("AI conversation handoff could not be delivered or stored for retry");
    }
  }
  if (section) {
    void Events.Emit("ai:open-settings", { section });
  }
  const deliveredTarget = outcome?.target ?? staged.target;
  const acknowledged = outcome?.ack ?? findAIConversationTargetAck(deliveredTarget);
  return {
    requestId: deliveredTarget.requestId,
    sourceOrigin: deliveredTarget.sourceOrigin,
    sourceEpoch: deliveredTarget.sourceEpoch,
    sequence: deliveredTarget.sequence,
    recipientEpoch: deliveredTarget.recipientEpoch,
    receiverEpoch: acknowledged?.receiverEpoch ?? null,
    acknowledged: acknowledged !== undefined,
    durable: outcome?.durable ?? staged.durable,
  };
}

export async function sendSelectionToAIDesktopWindow(
  code: string,
  language: string,
  filePath: string,
): Promise<void> {
  if (!code) return;
  await openAIDesktopWindow(undefined, { requireConversationAck: true });
  await windowService.sendSelectionToAI(code, language, filePath);
}

/** Toggles visibility while routing every show/focus transition through handoff. */
export async function toggleAIDesktopWindow(): Promise<void> {
  let visible = false;
  try {
    visible = await windowService.isAIWindowVisible();
  } catch {
    await openAIDesktopWindow();
    return;
  }
  if (visible) {
    await windowService.toggleAIWindow();
    return;
  }
  await openAIDesktopWindow();
}

/** 切换交互模式。未完成的 Plan 输入入口已隐藏；旧状态恢复时降级到 Chat。 */
export function switchMode(mode: AiMode): void {
  const effectiveMode = mode === "plan" ? "chat" : mode;
  aiAssistantState.mode = effectiveMode;
  // Keep the backend-facing Agent mode in lockstep with the visible mode selector.
  setAgentMode(effectiveMode === "agent" ? "agent" : "chat");
}

/** 设置左侧会话列表宽度（拖拽调整）。限制 200–480px 避免过窄/过宽。 */
export function setSidebarWidth(px: number): void {
  const clamped = Math.min(480, Math.max(200, px));
  aiAssistantState.sidebarWidth = clamped;
}

/** 折叠/展开右侧上下文面板。 */
export function toggleContextPanel(): void {
  aiAssistantState.contextPanelCollapsed = !aiAssistantState.contextPanelCollapsed;
}

/** 同步当前活动会话到独立页面状态（会话切换时调用）。 */
export function setActiveConversation(id: string | null): void {
  aiAssistantState.activeConversationId = id;
}

/** 返回独立页面是否已就绪（有活动会话或处于初始 chat 模式）。 */
export function isStandaloneReady(): boolean {
  return aiAssistantState.mode !== null;
}
