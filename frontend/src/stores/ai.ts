// Koyori IDE 模块 · Ai；交互服务：AI 对话（AIService）、对话历史（ConversationService）。
// 喵，这是 Koyori IDE 的 Ai 模块（前端实现）~
import { reactive } from "vue";
import { Events } from "@wailsio/runtime";
import { aiService, conversationService } from "@/api/services";
import { appState, activeAIConfig } from "@/stores/app";
import { notifyError } from "@/lib/notifications";
import { pushOutput } from "@/stores/output";
import {
  agentState,
  getAgentSystemPrompt,
  onAssistantFinished,
  onNativeToolCalls,
  buildNativeToolDefs,
  type NativeToolCallPayload,
} from "@/stores/agent";
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
import type { ChatMessage, AIContextAttachment, AIActionName, FileContextEntry, ContextChip } from "@/types";

export type AIStoreMessage = ChatMessage & { id: string };

function createMessage(
  role: ChatMessage["role"],
  content: string,
  images?: string[],
): AIStoreMessage {
  return images && images.length > 0
    ? { role, content, images, id: crypto.randomUUID() }
    : { role, content, id: crypto.randomUUID() };
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
let streamGeneration = 0;

// M-16: 流式超时兜底定时器。若 ai:done/ai:error 在超时窗口内未到达，
// 强制清理 streaming 状态，避免后端静默丢失事件导致 UI 永久卡死。
// 导出常量便于测试引用（测试中配合 vi.useFakeTimers 推进时间）。
export const STREAM_TIMEOUT_MS = 5 * 60 * 1000; // 5 分钟
let streamTimeoutTimer: ReturnType<typeof setTimeout> | null = null;

function clearStreamTimeout(): void {
  if (streamTimeoutTimer !== null) {
    clearTimeout(streamTimeoutTimer);
    streamTimeoutTimer = null;
  }
}

export function resetStreamState(): void {
  clearStreamTimeout();
  streamGeneration += 1;
  if (pendingAssistantMessage && pendingAssistantMessage.content === "") {
    const idx = aiState.messages.indexOf(pendingAssistantMessage);
    if (idx >= 0) aiState.messages.splice(idx, 1);
  }
  pendingAssistantMessage = null;
  aiState.streaming = false;
  aiState.globalStreamBusy = false;
  aiState.activeStreamId = null;
  aiState.conversationStaleWhileStreaming = false;
}

function startStreamTimeout(): void {
  clearStreamTimeout();
  streamTimeoutTimer = setTimeout(() => {
    streamTimeoutTimer = null;
    // 超时兜底：后端既未发 ai:done 也未发 ai:error，强制清理本地状态。
    if (aiState.streaming || pendingAssistantMessage) {
      const errMsg = "AI stream timed out";
      aiState.error = errMsg;
      if (pendingAssistantMessage && pendingAssistantMessage.content === "") {
        const idx = aiState.messages.indexOf(pendingAssistantMessage);
        if (idx >= 0) aiState.messages.splice(idx, 1);
      }
      pendingAssistantMessage = null;
      aiState.streaming = false;
      aiState.activeStreamId = null;
      aiState.globalStreamBusy = false;
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

export function handleAIChunkEvent(event: unknown): void {
  if (!canHandleAIEvent()) return;
  const { streamId, data } = parseAIStreamPayload(event);
  if (!isOwnedStreamEvent(streamId)) return;
  if (pendingAssistantMessage && typeof data === "string") {
    pendingAssistantMessage.content += data;
  }
}

export function handleAIDoneEvent(event: unknown): void {
  if (!canHandleAIEvent()) return;
  const { streamId } = parseAIStreamPayload(event);
  if (!isOwnedStreamEvent(streamId)) return;
  clearStreamTimeout();
  if (!pendingAssistantMessage && !aiState.streaming) {
    aiState.activeStreamId = null;
    return;
  }
  const lastContent = pendingAssistantMessage?.content ?? "";
  if (pendingAssistantMessage && lastContent === "") {
    const idx = aiState.messages.indexOf(pendingAssistantMessage);
    if (idx >= 0) aiState.messages.splice(idx, 1);
  }
  pendingAssistantMessage = null;
  aiState.streaming = false;
  aiState.activeStreamId = null;
  void persistConversation();
  if (agentState.mode === "agent" && lastContent) {
    onAssistantFinished(lastContent);
  }
}

export function handleAIErrorEvent(event: unknown): void {
  if (!canHandleAIEvent()) return;
  const { streamId, data } = parseAIStreamPayload(event);
  if (!isOwnedStreamEvent(streamId)) return;
  clearStreamTimeout();
  if (!pendingAssistantMessage && !aiState.streaming) {
    aiState.activeStreamId = null;
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
  aiState.streaming = false;
  aiState.activeStreamId = null;
  aiState.conversationStaleWhileStreaming = false;
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
  const { streamId, data, raw } = parseAIStreamPayload(event);
  if (!isOwnedStreamEvent(streamId)) return;
  let payload: unknown = data || raw;
  if (payload && typeof payload === "object" && "data" in (payload as object)) {
    payload = (payload as { data?: unknown }).data;
  }
  let parsed: NativeToolCallPayload[] = [];
  try {
    if (typeof payload === "string") parsed = JSON.parse(payload) as NativeToolCallPayload[];
    else if (Array.isArray(payload)) parsed = payload as NativeToolCallPayload[];
  } catch {
    return;
  }
  if (parsed.length > 0) onNativeToolCalls(parsed);
}

export function handleConversationSavedEvent(event: unknown): void {
  if (!canHandleAIEvent()) return;
  const payload = unwrapEventData(event);
  const origin = parseSyncOrigin(payload);
  if (origin && origin === getWindowOriginId()) return;
  const id = payload && typeof payload === "object" && "id" in payload
    ? String((payload as { id?: unknown }).id ?? "")
    : "";
  if (!id || aiState.currentConversationId !== id) return;
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
        // Plan 11 Task 4: mcp chip 附加命名空间，提示 AI 可调用此工具。
        case "mcp":
          prefix += `MCP tool: ${chip.label}\n\n`;
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
export async function sendMessage(content: string): Promise<void> {
  if (aiState.streaming) return;
  // prompt-5 Task B: another window owns the process-wide stream.
  if (aiState.globalStreamBusy) {
    notifyError(
      translate("aiChat.streamBusy"),
      translate("aiChat.streamBusyTitle"),
    );
    return;
  }
  ensureAIEventListeners();
  const generation = ++streamGeneration;

  aiState.error = null;
  const fullContent = buildUserMessage(content);
  // C-6: 生成稳定 id 替代数组索引作 v-for key，避免 FIFO drop 时 DOM 错位。
  // G12: image chips travel as structured attachments on this user message.
  const imageUrls = aiState.contextChips
    .filter((chip) => chip.kind === "image" && chip.imageUrl)
    .map((chip) => chip.imageUrl as string);
  aiState.messages.push(createMessage("user", fullContent, imageUrls));
  clearMentionedFiles();
  clearContextChips();
  aiState.streaming = true;
  // Optimistic busy until backend ai:stream-busy confirms (prompt-6 Task 5 allows optimistic UI).
  aiState.globalStreamBusy = true;

  const assistantMessage = createMessage("assistant", "");
  aiState.messages.push(assistantMessage);
  pendingAssistantMessage = assistantMessage;
  trimMessagesIfNeeded();

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
    aiService.setConfig({
      // G-SEC-07: use the stored key (backend fetches from SettingsService).
      // The plaintext key never crosses the Wails binding.
      useStoredKey: true,
      configId: appState.activeAIConfigId,
      baseUrl: appState.aiBaseUrl,
      model: appState.aiModel,
      systemPrompt,
      // Plan 54: pass prompt overrides so the backend's GetEffective*
      // methods return the user-configured prompt instead of the built-in.
      agentSystemPrompt: appState.aiAgentSystemPrompt,
      conversationTitlePrompt: appState.aiConversationTitlePrompt,
      inlineCompletionPrompt: appState.aiInlineCompletionPrompt,
      temperature: appState.temperature,
      protocol: activeCfg?.protocol ?? "openai",
      maxTokens: appState.maxTokens,
      // prompt-5 Task H: native tools in agent mode (fence remains dual-track).
      tools: agentState.mode === "agent" ? buildNativeToolDefs() : [],
    });
    const history = aiState.messages.slice(0, -1);
    // prompt-6 Task 2: StartStream returns streamId for event routing.
    const streamId = await aiService.startStream(history);
    if (
      generation !== streamGeneration ||
      pendingAssistantMessage !== assistantMessage ||
      !aiState.streaming
    ) {
      return;
    }
    if (typeof streamId === "string" && streamId) {
      aiState.activeStreamId = streamId;
    }
    // M-16: 流已发起，启动超时兜底定时器。若后端在 STREAM_TIMEOUT_MS 内
    // 既未发 ai:done 也未发 ai:error，强制清理本地 streaming 状态。
    startStreamTimeout();
    // Stream continues async; ai:done/ai:error events handle completion
  } catch (e: unknown) {
    if (generation !== streamGeneration) return;
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
    if (assistantMessage.content === "") {
      aiState.messages.pop();
    }
    // Also remove the user message we optimistically added if stream never started.
    const lastUser = aiState.messages[aiState.messages.length - 1];
    if (lastUser?.role === "user" && lastUser.content === fullContent) {
      // Keep user message so they can resend; only clear streaming flags.
    }
    pendingAssistantMessage = null;
    aiState.streaming = false;
    aiState.activeStreamId = null;
    // Only clear optimistic busy if backend never confirmed ownership.
    aiState.globalStreamBusy = false;
  }
}

/**
 * Stops an in-progress streaming request.
 * prompt-6 Task 5: do not clear globalStreamBusy locally — StopStream on the
 * backend emits ai:stream-busy=false. Local streaming flag ends so UI unlocks.
 */
export async function stopGeneration(): Promise<void> {
  try {
    await aiService.stopStream();
  } catch {
    // Ignore errors when stopping
  }
  // M-16: 手动停止也清除超时兜底定时器。
  clearStreamTimeout();
  aiState.streaming = false;
  aiState.activeStreamId = null;
  pendingAssistantMessage = null;
  // globalStreamBusy cleared by ai:stream-busy event from backend.
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
export function clearMessages(): void {
  if (aiState.streaming) return;
  aiState.messages = [];
  aiState.error = null;
  aiState.currentConversationId = null;
  aiState.currentConversationTitle = null;
  aiState.conversationRevision = 0;
  aiState.conversationStaleWhileStreaming = false;
  aiState.context = null;
  // N-60: Reset per-session system prompt override.
  aiState.currentSystemPromptOverride = null;
}

/**
 * Deletes a single message by index. If the conversation has only one message
 * left, it behaves like clearMessages. Persists the updated conversation.
 */
export async function deleteMessage(index: number): Promise<void> {
  if (aiState.streaming) return;
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
  if (aiState.streaming) return [];
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
async function persistConversation(): Promise<void> {
  if (aiState.messages.length === 0) return;
  try {
    const wasNew = !aiState.currentConversationId;
    let id = aiState.currentConversationId;
    if (!id) {
      id = await conversationService.generateId();
      aiState.currentConversationId = id;
      aiState.conversationRevision = 0;
    }
    // Generate a title only for new conversations; reuse the existing title
    // for subsequent persists to avoid redundant API calls and title churn.
    let title = aiState.currentConversationTitle;
    if (wasNew || !title) {
      const firstUser = aiState.messages.find((m) => m.role === "user");
      if (firstUser) {
        // prompt-7 Task E / BUG-M13: skip AI title when stream busy / streaming.
        const skipAiTitle =
          aiState.streaming ||
          aiState.globalStreamBusy ||
          aiState.activeStreamId != null;
        try {
          if (skipAiTitle) {
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
      aiState.currentConversationTitle = title;
    }
    const baseRev = aiState.conversationRevision;
    const payload: {
      id: string;
      title: string;
      created_at: number;
      messages: { role: string; content: string }[];
      system_prompt_override?: string;
      expected_revision?: number;
    } = {
      id,
      title,
      created_at: Math.floor(Date.now() / 1000),
      messages: aiState.messages.map((m) => ({ role: m.role, content: m.content })),
      system_prompt_override: aiState.currentSystemPromptOverride ?? undefined,
    };
    // CAS only when we already know a disk revision (not brand-new id).
    if (!wasNew && baseRev > 0) {
      payload.expected_revision = baseRev;
    }
    try {
      await conversationService.save(payload);
      // Optimistic bump; next load will SSOT.
      aiState.conversationRevision = baseRev > 0 ? baseRev + 1 : 1;
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
          messages: aiState.messages.map((m) => ({ role: m.role, content: m.content })),
          system_prompt_override: aiState.currentSystemPromptOverride ?? undefined,
        });
        aiState.currentConversationId = forkedId;
        aiState.currentConversationTitle = forkTitle;
        aiState.conversationRevision = 1;
        id = forkedId;
        title = forkTitle;
      } else {
        throw saveErr;
      }
    }
    // prompt-6 Task 1: notify peer webviews to refresh conversation list / reload.
    try {
      void Events.Emit("conversation:saved", {
        origin: getWindowOriginId(),
        id,
        title,
        revision: aiState.conversationRevision,
        at: Date.now(),
      });
    } catch {
      // Events may be unavailable in unit tests.
    }
  } catch (e) {
    console.error("Failed to persist conversation:", e);
  }
}

/**
 * Loads a saved conversation into the chat.
 */
export async function loadConversation(id: string): Promise<void> {
  try {
    const conv = await conversationService.load(id);
    // C-6: 为加载的持久化消息生成稳定 id，保证 v-for key 稳定。
    aiState.messages = conv.messages.map((m) =>
      createMessage(m.role as ChatMessage["role"], m.content),
    );
    aiState.currentConversationId = conv.id;
    aiState.currentConversationTitle = conv.title;
    aiState.conversationRevision = conv.revision ?? 0;
    aiState.conversationStaleWhileStreaming = false;
    // N-60: Restore the per-conversation system prompt override.
    aiState.currentSystemPromptOverride = conv.system_prompt_override ?? null;
    aiState.error = null;
  } catch (e: unknown) {
    aiState.error = errorMessage(e) || "Failed to load conversation";
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
