<script setup lang="ts">
// Koyori IDE 组件 · Ai Chat Panel；交互服务：AI 对话（AIService）、对话历史（ConversationService）、文件系统（FileService）。
// 喵，这是 Ai Chat Panel，负责 Koyori IDE 的界面呈现喵~
import { appState, toggleAiChat, saveSettings, activateAIConfig } from "@/stores/app";
import { computed, ref, nextTick, onBeforeUnmount, onMounted, watch } from "vue";
import { VueMonacoDiffEditor } from "@guolao/vue-monaco-editor";
import MarkdownContent from "@/components/common/MarkdownContent.vue";
import AgentExecutionTimeline from "@/components/ai-assistant/AgentExecutionTimeline.vue";
import { agentTimelineState } from "@/stores/agentTimeline";
import FocusTrapDialog from "@/components/common/FocusTrapDialog.vue";
import { Close, Promotion, VideoPause, CopyDocument, ChatDotRound, Clock, Edit, Aim, Document, Search, VideoPlay, EditPen, Check, Close as CloseIcon, List, Setting, MagicStick, FullScreen, Monitor, Back, Delete, More } from "@element-plus/icons-vue";
import { ElMessageBox } from "element-plus";
import { aiState, sendMessage, clearMessages, stopGeneration, clearContext, loadConversation, addMentionedFile, removeMentionedFile, renameConversation, setSystemPromptOverride, deleteMessage, revokeMessagesFrom } from "@/stores/ai";
import { openStandalonePage, openAIDesktopWindow, switchMode as switchAssistantMode } from "@/stores/aiAssistant";
import {
  agentState,
  isAgentMode,
  extractToolCallBlocks,
  approveAndFeed,
  rejectAndFeed,
  clearPendingToolCalls,
  getRegisteredTools,
  type ToolCall,
  type ToolCallKind,
} from "@/stores/agent";
import type { RiskLevel, ChatMessage } from "@/types";
import { conversationService, fileService } from "@/api/services";
import { activeFile, updateContent } from "@/stores/editor";
import type { Conversation, RulesFileCandidate } from "@/types";
import {
  rulesState,
  rules as currentRules,
  hasRules,
  rulesFileCount,
  loadRules as reloadRules,
  saveRules,
  saveRulesConfig,
  makeDefaultRulesConfig,
} from "@/stores/rules";
import type { RulesConfig, RulesCandidateConfig } from "@/types";
import { renderMarkdownWithApplyButtons } from "@/lib/markdown";
import { detectLanguage } from "@/lib/language";
import { getMonacoThemeName } from "@/lib/monaco-themes";
import { getSuggestedModels, getProviderPreset } from "@/lib/aiProviders";
import { notifyError, notifySuccess, notifyWarning } from "@/lib/notifications";
import { errorMessage } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import { aiService } from "@/api/services";
import { Refresh } from "@element-plus/icons-vue";
import { connectivityState } from "@/lib/connectivity";

const { t } = useI18n();

// embedded 模式：嵌入 SidePanel 内部，不依赖 aiChatVisible，width 占满父容器。
const props = withDefaults(defineProps<{ embedded?: boolean }>(), {
  embedded: false,
});

const isVisible = computed(() => props.embedded || appState.aiChatVisible);
// N-20: bind width to appState so the drag handle can resize the panel.
const panelWidthPx = computed(() => `${appState.aiChatWidth}px`);
const inputText = ref("");
const messageListRef = ref<HTMLElement | null>(null);

const showFilePicker = ref(false);
const manualFilePath = ref("");

// N-50/Proposal S: Model suggestions come from the active provider's preset
// (offline fallback) or from the online-refreshed list. The user can also
// type any custom model name via el-select allow-create.
const onlineModels = ref<string[]>([]);
const refreshingModels = ref(false);

const modelOptions = computed(() => {
  // Prefer online-refreshed models; fall back to preset suggestions.
  const list = onlineModels.value.length > 0
    ? onlineModels.value
    : getSuggestedModels(appState.aiProvider);
  return list.map((m) => ({ label: m, value: m }));
});

// N-50/Proposal S: Refresh the model list from the provider's /v1/models endpoint.
async function refreshModelList() {
  if (refreshingModels.value) return;
  const provider = getProviderPreset(appState.aiProvider);
  // Determine baseURL: use the preset's baseUrl, or the user's custom baseUrl.
  const baseURL = provider?.baseUrl || appState.aiBaseUrl;
  if (!baseURL) {
    notifyWarning(t("aiChat.refreshModelsNoUrl"));
    return;
  }
  refreshingModels.value = true;
  try {
    // CRIT-01/G-SEC-07: pass an empty apiKey so the backend uses the stored
    // key (a.config.APIKey, populated via SetConfig's UseStoredKey path).
    // The frontend never holds plaintext keys.
    const models = await aiService.listModels(baseURL, "");
    if (models.length > 0) {
      onlineModels.value = models;
      notifySuccess(t("aiChat.refreshModelsSuccess", { count: models.length }));
    } else {
      notifyWarning(t("aiChat.refreshModelsEmpty"));
    }
  } catch (e) {
    notifyError(t("aiChat.refreshModelsError", { error: errorMessage(e) }));
  } finally {
    refreshingModels.value = false;
  }
}

// Clear online models when the provider changes so stale models from
// a different provider don't persist in the dropdown.
watch(() => appState.aiProvider, () => {
  onlineModels.value = [];
});

const selectedModel = computed({
  get: () => appState.aiModel ?? "gpt-4o",
  set: (val: string) => {
    appState.aiModel = val;
    // Also update the active provider config's model so switching configs
    // preserves the user's model choice per-config.
    const active = appState.aiProviderConfigs.find((c) => c.id === appState.activeAIConfigId);
    if (active) {
      active.model = val;
    }
    saveSettings();
  },
});

// CC Switch-style: switch the active AI provider config from the chat header.
function handleConfigSwitch(id: string): void {
  activateAIConfig(id);
  // Clear online models so the model dropdown refreshes for the new provider.
  onlineModels.value = [];
}

const hasMessages = computed(() => aiState.messages.length > 0);
const hasContext = computed(() => aiState.context !== null);

const showHistory = ref(false);
const conversations = ref<Conversation[]>([]);

// N-60: Per-conversation system prompt override UI state.
const showSystemPromptPopover = ref(false);
const systemPromptDraft = ref("");
const hasSystemPromptOverride = computed(() => aiState.currentSystemPromptOverride !== null);

function openSystemPromptPopover(): void {
  // Initialize the draft from the current override (or empty for new).
  systemPromptDraft.value = aiState.currentSystemPromptOverride ?? "";
  showSystemPromptPopover.value = true;
}

function applySystemPromptOverride(): void {
  setSystemPromptOverride(systemPromptDraft.value);
  showSystemPromptPopover.value = false;
}

function resetSystemPromptOverride(): void {
  setSystemPromptOverride(null);
  systemPromptDraft.value = "";
  showSystemPromptPopover.value = false;
}

async function toggleHistory() {
  showHistory.value = !showHistory.value;
  if (showHistory.value) {
    try {
      // Plan 11 Task 2: exclude soft-deleted conversations (DeletedAt > 0)
      // from the inline history list. The trash view lives in ConversationSidebar.
      const all = await conversationService.list();
      conversations.value = all.filter((c) => !c.deleted_at);
    } catch (e) {
      console.error("Failed to load conversations:", e);
    }
  }
}

async function handleLoadConversation(id: string) {
  const loaded = await loadConversation(id);
  if (loaded !== false) showHistory.value = false;
}

async function handleDeleteConversation(id: string) {
  try {
    await conversationService.delete(id);
    conversations.value = conversations.value.filter((c) => c.id !== id);
  } catch (e) {
    console.error("Failed to delete conversation:", e);
  }
}

async function handleRenameConversation() {
  const convId = aiState.currentConversationId;
  if (!convId) {
    notifyWarning(t("aiChat.noActiveConversation"));
    return;
  }
  try {
    const { value } = await ElMessageBox.prompt(t("aiChat.renamePrompt"), t("aiChat.renameTitle"), {
      confirmButtonText: t("aiChat.rename"),
      cancelButtonText: t("common.cancel"),
      inputPattern: /.+/,
      inputErrorMessage: t("aiChat.renameErrorEmpty"),
    });
    if (value) {
      const success = await renameConversation(convId, value);
      if (success) {
        notifySuccess(t("aiChat.renamed"));
      }
    }
  } catch {
    // User cancelled — no action needed
  }
}

function formatTime(ts: number): string {
  return new Date(ts * 1000).toLocaleString();
}

// --- Agent mode helpers ---
const pendingToolCalls = computed(() => agentState.pendingToolCalls);
const agentTurnBusy = computed(() => aiState.streaming || aiState.globalStreamBusy);
// G-SEC-02: show the denylist warning banner when there is at least one
// pending `run` tool call — shell commands always require manual approval.
const hasPendingRunToolCalls = computed(() =>
  pendingToolCalls.value.some((tc) => tc.kind === "run"),
);

function toolCallIcon(kind: ToolCallKind) {
  switch (kind) {
    case "read": return Document;
    case "write": return EditPen;
    case "run": return VideoPlay;
    case "search": return Search;
		default: return MagicStick;
  }
}

function structuredArguments(tc: ToolCall): string {
	return JSON.stringify(tc.arguments ?? {}, null, 2);
}

function toolCallStatusLabel(tc: ToolCall): string {
  switch (tc.status) {
    case "pending": return t("aiChat.statusPending");
    case "approved": return t("aiChat.statusApproved");
    case "rejected": return t("aiChat.statusRejected");
    case "executed": return t("aiChat.statusExecuted");
    case "error": return t("aiChat.statusError");
  }
}

// Build a kind → dangerLevel map from the tool registry (N-16). Used to show
// a risk badge for non-`run` tools that don't have a runtime riskLevel.
const toolDangerLevelMap = computed(() => {
  const m = new Map<string, RiskLevel>();
  for (const t of getRegisteredTools()) {
    if (t.schema.dangerLevel) {
      m.set(t.kind, t.schema.dangerLevel);
    }
  }
  return m;
});

// effectiveRiskLevel returns the risk level to display in the approval UI:
// - `run` tools: use the runtime riskLevel from checkRunRisk (N-1) if available
// - Other tools: fall back to the schema's dangerLevel (N-16)
function effectiveRiskLevel(tc: ToolCall): RiskLevel | undefined {
  if (tc.riskLevel) return tc.riskLevel;
  return toolDangerLevelMap.value.get(tc.kind);
}

function riskBadgeLabel(level: RiskLevel | undefined): string {
  switch (level) {
    case "safe": return t("aiChat.riskSafe");
    case "elevated": return t("aiChat.riskElevated");
    case "dangerous": return t("aiChat.riskDangerous");
    default: return "";
  }
}

// Pure fenced compatibility calls are represented by approval cards, so hide
// those valid blocks from normal Markdown. If a response already contains
// native tool calls, retain any extra fenced text to expose protocol mixing.
const cleanedMessageCache = computed(() =>
  aiState.messages.map((m) => {
    if (
      m.role !== "assistant"
      || !isAgentMode.value
      || (m.toolCalls?.length ?? 0) > 0
    ) return m.content;
    return extractToolCallBlocks(m.content).cleanedMessage;
  }),
);

function handleApprove(tc: ToolCall) {
  if (agentTurnBusy.value) {
    notifyWarning(t("aiChat.waitForResponse"));
    return;
  }
  void approveAndFeed(tc);
}

function handleReject(tc: ToolCall) {
  if (agentTurnBusy.value) {
    notifyWarning(t("aiChat.waitForResponse"));
    return;
  }
  void rejectAndFeed(tc);
}

function handleClearPendingToolCalls(): void {
  if (agentTurnBusy.value) {
    notifyWarning(t("aiChat.waitForResponse"));
    return;
  }
  clearPendingToolCalls();
}

function handleModeToggle() {
  switchAssistantMode(isAgentMode.value ? "chat" : "agent");
}

// P2-13: ⋯ more 菜单统一分发次级操作，保持原有功能与体验不变。
function handleMoreCommand(command: string): void {
  switch (command) {
    case "refresh":
      void refreshModelList();
      break;
    case "expand":
      openStandalonePage();
      break;
    case "aiwindow":
      openAIDesktopWindow();
      break;
    case "rename":
      void handleRenameConversation();
      break;
    case "history":
      void toggleHistory();
      break;
  }
}

// --- Rules modal (#25) ---
const rulesModalVisible = ref(false);
const rulesDraft = ref("");
const rulesDraftPath = ref("");
const rulesSaving = ref(false);

const rulesBadgeLabel = computed(() => {
  if (!hasRules.value) return t("aiChat.rulesNone");
  if (rulesFileCount.value > 1) return t("aiChat.rulesFiles", { count: rulesFileCount.value });
  const src = currentRules.value?.source ?? "";
  return src ? t("aiChat.rulesSource", { source: src }) : t("aiChat.rulesLabel");
});

const rulesCandidateOptions = computed(() =>
  rulesState.candidates.map((c: RulesFileCandidate) => ({
    label: `${c.path}${c.exists ? " (exists)" : ""}`,
    value: c.path,
  })),
);

function openRulesModal() {
  rulesDraft.value = currentRules.value?.content ?? "";
  // Default to the loaded rules path, or the first existing candidate, or the first candidate.
  rulesDraftPath.value =
    currentRules.value?.path ??
    rulesState.candidates.find((c) => c.exists)?.path ??
    rulesState.candidates[0]?.path ??
    ".koyori-ide/rules.md";
  rulesModalVisible.value = true;
}

function cancelRulesEdit() {
  rulesModalVisible.value = false;
  rulesDraft.value = "";
  rulesDraftPath.value = "";
}

async function handleSaveRules() {
  if (!appState.currentProject) {
    notifyError(t("aiChat.noProjectOpen"));
    return;
  }
  rulesSaving.value = true;
  try {
    const ok = await saveRules(appState.currentProject, rulesDraft.value, rulesDraftPath.value);
    if (ok) {
      rulesModalVisible.value = false;
      rulesDraft.value = "";
      rulesDraftPath.value = "";
    }
  } finally {
    rulesSaving.value = false;
  }
}

async function handleReloadRules() {
  if (!appState.currentProject) return;
  await reloadRules(appState.currentProject);
  rulesDraft.value = currentRules.value?.content ?? "";
}

// --- Rules config (N-18) ---
const rulesConfigVisible = ref(false);
const rulesConfigDraft = ref<RulesConfig>(makeDefaultRulesConfig());
const rulesConfigSaving = ref(false);
const rulesCandidateKeys = new WeakMap<RulesCandidateConfig, string>();
let rulesCandidateKeySequence = 0;

function rulesCandidateKey(candidate: RulesCandidateConfig): string {
  const existing = rulesCandidateKeys.get(candidate);
  if (existing) return existing;
  const key = `rules-candidate-${++rulesCandidateKeySequence}`;
  rulesCandidateKeys.set(candidate, key);
  return key;
}

function openRulesConfig() {
  // Start from the loaded config, or a blank default.
  const cfg = rulesState.config;
  rulesConfigDraft.value = {
    mode: cfg?.mode ?? "first",
    candidates: (cfg?.candidates ?? []).map((c) => ({ ...c })),
  };
  rulesConfigVisible.value = true;
}
// Exposed for template / command palette hooks (eslint may not see template use).
void openRulesConfig;

function addRulesCandidate() {
  rulesConfigDraft.value.candidates = [
    ...(rulesConfigDraft.value.candidates ?? []),
    { path: "", source: "" },
  ];
}

function removeRulesCandidate(idx: number) {
  const list = [...(rulesConfigDraft.value.candidates ?? [])];
  list.splice(idx, 1);
  rulesConfigDraft.value.candidates = list;
}

async function handleSaveRulesConfig() {
  if (!appState.currentProject) {
    notifyError(t("aiChat.noProjectOpen"));
    return;
  }
  // Filter out candidates with empty paths.
  const cfg: RulesConfig = {
    mode: rulesConfigDraft.value.mode || "first",
    candidates: (rulesConfigDraft.value.candidates ?? []).filter(
      (c: RulesCandidateConfig) => c.path.trim() !== "",
    ),
  };
  rulesConfigSaving.value = true;
  try {
    const ok = await saveRulesConfig(appState.currentProject, cfg);
    if (ok) {
      rulesConfigVisible.value = false;
    }
  } finally {
    rulesConfigSaving.value = false;
  }
}

function contextLabel(): string {
  if (!aiState.context) return "";
  const c = aiState.context;
  const name = c.filePath.split("/").pop() ?? c.filePath;
  return c.kind === "selection" ? `${name}:${c.startLine}-${c.endLine}` : name;
}

function renderContent(content: string): string {
  return renderMarkdownWithApplyButtons(content);
}

function boundedNativeToolText(value: string, limit = 640): string {
  return value.length > limit ? `${value.slice(0, limit)}\n...` : value;
}

// --- Side diff apply (#12) ---
const diffModalVisible = ref(false);
const diffOriginal = ref("");
const diffModified = ref("");
const diffTargetPath = ref("");
const diffTargetLanguage = ref("");

const diffMonacoTheme = computed(() => getMonacoThemeName(appState.accentTheme));

function handleContentClick(e: MouseEvent) {
  const target = e.target as HTMLElement;
  if (!target || !target.classList.contains("code-block-apply-btn")) return;
  const wrap = target.closest(".code-block-wrap") as HTMLElement | null;
  const pre = wrap?.querySelector("pre");
  if (!pre) return;
  const code = pre.textContent ?? "";
  openApplyDiff(code);
}

function openApplyDiff(code: string) {
  const file = activeFile.value;
  if (!file) {
    notifyWarning(t("aiChat.openFileToApply"));
    return;
  }
  diffOriginal.value = file.content;
  diffModified.value = code;
  diffTargetPath.value = file.path;
  diffTargetLanguage.value = file.language || detectLanguage(file.path);
  diffModalVisible.value = true;
}

function cancelApplyDiff() {
  diffModalVisible.value = false;
  diffOriginal.value = "";
  diffModified.value = "";
  diffTargetPath.value = "";
}

function confirmApplyDiff() {
  if (!diffTargetPath.value) return;
  updateContent(diffTargetPath.value, diffModified.value);
  notifySuccess(t("aiChat.appliedTo", { name: diffTargetPath.value.split(/[/\\]/).pop() ?? diffTargetPath.value }));
  cancelApplyDiff();
}

async function handleSend() {
  const text = inputText.value.trim();
  if (!text || aiState.streaming || aiState.globalStreamBusy) return;
  const accepted = await sendMessage(text);
  if (!accepted) return;
  inputText.value = "";
  await nextTick();
  scrollToBottom();
}

async function sendSuggestion(text: string) {
  if (aiState.streaming || aiState.globalStreamBusy) return;
  await sendMessage(text);
  await nextTick();
  scrollToBottom();
}

function handleKeydown(e: KeyboardEvent) {
  if (e.key === "Enter" && !e.shiftKey) {
    e.preventDefault();
    handleSend();
  }
}

async function handleAddFileByPath() {
  const filePath = manualFilePath.value.trim();
  if (!filePath) return;
  try {
    const content = await fileService.readFile(filePath);
    const ext = filePath.split(".").pop() ?? "";
    const languageMap: Record<string, string> = {
      ts: "typescript", tsx: "typescript", js: "javascript", jsx: "javascript",
      go: "go", py: "python", rs: "rust", java: "java",
      md: "markdown", json: "json", yaml: "yaml", yml: "yaml",
      html: "html", css: "css", vue: "vue",
    };
    const language = languageMap[ext] ?? "plaintext";
    addMentionedFile({ filePath, language, content });
    notifySuccess(t("aiChat.fileAdded", { path: filePath }));
    manualFilePath.value = "";
    showFilePicker.value = false;
  } catch (e: unknown) {
    notifyError(t("aiChat.readFileFailed", { error: errorMessage(e) }));
  }
}

function handleStop() {
  stopGeneration();
}

function handleCopy(content: string) {
  navigator.clipboard.writeText(content).catch(() => {});
}

// --- 右键菜单：复制 / 撤回 / 删除 ---
const messageContextMenu = ref<{ visible: boolean; x: number; y: number; index: number; msg: ChatMessage | null }>({
  visible: false,
  x: 0,
  y: 0,
  index: -1,
  msg: null,
});

function openMessageContextMenu(e: MouseEvent | KeyboardEvent, index: number, msg: ChatMessage): void {
  e.preventDefault();
  const target = e.currentTarget as HTMLElement | null;
  const rect = target?.getBoundingClientRect();
  const isMouseEvent = e instanceof MouseEvent;
  messageContextMenu.value = {
    visible: true,
    x: isMouseEvent ? e.clientX : (rect?.left ?? 0) + 16,
    y: isMouseEvent ? e.clientY : (rect?.top ?? 0) + 16,
    index,
    msg,
  };
}

function onMessageKeydown(e: KeyboardEvent, index: number, msg: ChatMessage): void {
  if ((e.shiftKey && e.key === "F10") || e.key === "ContextMenu") {
    openMessageContextMenu(e, index, msg);
  }
}

function closeMessageContextMenu(): void {
  messageContextMenu.value.visible = false;
}

function handleDocumentClick(): void {
  if (messageContextMenu.value.visible) closeMessageContextMenu();
}

onMounted(() => {
  document.addEventListener("click", handleDocumentClick);
});

onBeforeUnmount(() => {
  document.removeEventListener("click", handleDocumentClick);
});

function ctxCopyMessage(): void {
  if (!messageContextMenu.value.msg) return;
  navigator.clipboard.writeText(messageContextMenu.value.msg.content).catch(() => {});
  notifySuccess(t("aiChat.copiedToClipboard"));
  closeMessageContextMenu();
}

async function ctxDeleteMessage(): Promise<void> {
  const idx = messageContextMenu.value.index;
  closeMessageContextMenu();
  if (idx < 0) return;
  try {
    await deleteMessage(idx);
    notifySuccess(t("aiChat.messageDeleted"));
  } catch (e: unknown) {
    notifyError(errorMessage(e));
  }
}

async function ctxRevokeFromHere(): Promise<void> {
  const idx = messageContextMenu.value.index;
  closeMessageContextMenu();
  if (idx < 0) return;
  try {
    const revoked = await revokeMessagesFrom(idx);
    notifySuccess(t("aiChat.messagesRevoked", { count: revoked.length }));
  } catch (e: unknown) {
    notifyError(errorMessage(e));
  }
}

const BODY_FOLLOW_THRESHOLD_PX = 48;
const bodyFollowsLatest = ref(true);

function bodyIsNearBottom(element: HTMLElement): boolean {
  return element.scrollHeight - element.clientHeight - element.scrollTop <= BODY_FOLLOW_THRESHOLD_PX;
}

function onMessageBodyScroll(): void {
  const element = messageListRef.value;
  if (element) bodyFollowsLatest.value = bodyIsNearBottom(element);
}

function scrollToBottom() {
  if (messageListRef.value) {
    messageListRef.value.scrollTop = messageListRef.value.scrollHeight;
    bodyFollowsLatest.value = true;
  }
}

watch(
  () => aiState.messages.length,
  () => {
    nextTick(scrollToBottom);
  },
);

watch(
  () => aiState.messages[aiState.messages.length - 1]?.content,
  () => {
    nextTick(scrollToBottom);
  },
);

watch(
  () => [
    agentTimelineState.entries.map((entry) => [
      entry.id,
      entry.stage,
      entry.status ?? "",
      entry.updatedAt,
      entry.detail ?? "",
    ].join("\u0000")).join("\u0001"),
    pendingToolCalls.value.map((call) => [
      call.id,
      call.status,
      call.target,
      call.result ?? "",
      call.error ?? "",
    ].join("\u0000")).join("\u0001"),
  ] as const,
  () => {
    // Tool/reasoning activity is appended below the messages. Follow it only
    // when the user was already reading the newest content; manual history
    // scrolling must remain stable while a run continues in the background.
    const shouldFollow = bodyFollowsLatest.value;
    void nextTick(() => {
      if (shouldFollow) scrollToBottom();
    });
  },
);
</script>

<template>
  <transition name="slide-chat">
    <aside
      v-if="isVisible"
      class="ai-chat-panel"
      :class="{ 'ai-chat-panel--embedded': props.embedded }"
      :style="props.embedded ? { width: '100%', flex: '1' } : { width: panelWidthPx }"
      role="complementary"
      :aria-label="t('a11y.aiAssistantPanel')"
    >
      <div class="ai-chat-panel__header">
        <div class="ai-chat-panel__header-left">
          <el-icon :size="14"><ChatDotRound /></el-icon>
          <span class="ai-chat-panel__title">{{ t("aiChat.title") }}</span>
          <button
            type="button"
            class="ai-chat-panel__mode-toggle"
            :class="{ 'ai-chat-panel__mode-toggle--active': isAgentMode }"
            :aria-pressed="isAgentMode"
            :title="isAgentMode ? t('aiChat.modeAgentTitle') : t('aiChat.modeChatTitle')"
            @click="handleModeToggle"
          >
            <el-icon :size="12"><Aim /></el-icon>
            <span>{{ isAgentMode ? t('aiChat.modeAgent') : t('aiChat.modeChat') }}</span>
          </button>
          <button
            type="button"
            class="ai-chat-panel__rules-badge"
            :class="{ 'ai-chat-panel__rules-badge--active': hasRules }"
            :title="hasRules ? t('aiChat.rulesLoadedTitle', { count: rulesFileCount }) : t('aiChat.rulesNoneTitle')"
            @click="openRulesModal"
          >
            <el-icon :size="12"><List /></el-icon>
            <span>{{ rulesBadgeLabel }}</span>
          </button>
        </div>
        <div class="ai-chat-panel__header-right">
          <el-select
            :model-value="appState.activeAIConfigId"
            class="ai-chat-panel__config-select"
            size="small"
            :placeholder="t('aiChat.selectConfig')"
            :aria-label="t('aiChat.selectConfig')"
            @change="handleConfigSwitch"
          >
            <el-option
              v-for="cfg in appState.aiProviderConfigs"
              :key="cfg.id"
              :label="cfg.name"
              :value="cfg.id"
            />
          </el-select>
          <el-select
            v-model="selectedModel"
            class="ai-chat-panel__model-select"
            size="small"
            filterable
            allow-create
            default-first-option
            :placeholder="appState.aiModel || t('aiChat.selectModel')"
            :aria-label="t('aiChat.selectModel')"
          >
            <el-option
              v-for="model in modelOptions"
              :key="model.value"
              :label="model.label"
              :value="model.value"
            />
          </el-select>
          <!-- N-60: Per-conversation system prompt override -->
          <el-popover
            :visible="showSystemPromptPopover"
            placement="bottom-end"
            :width="360"
            trigger="manual"
          >
            <template #reference>
              <button
                type="button"
                class="ai-chat-panel__sysprompt"
                :class="{ 'ai-chat-panel__sysprompt--active': hasSystemPromptOverride }"
                :aria-label="t('aiChat.systemPromptOverride')"
                :title="t('aiChat.systemPromptOverride')"
                @click="openSystemPromptPopover"
              >
                <el-icon :size="14"><Setting /></el-icon>
              </button>
            </template>
            <div class="ai-chat-panel__sysprompt-popover">
              <p class="ai-chat-panel__sysprompt-hint">
                {{ t("aiChat.systemPromptOverrideHint") }}
              </p>
              <textarea
                v-model="systemPromptDraft"
                class="ai-chat-panel__sysprompt-textarea"
                rows="6"
                :placeholder="t('aiChat.systemPromptOverridePlaceholder')"
              />
              <div class="ai-chat-panel__sysprompt-actions">
                <el-button
                  v-if="hasSystemPromptOverride"
                  size="small"
                  @click="resetSystemPromptOverride"
                >
                  {{ t("aiChat.resetToGlobal") }}
                </el-button>
                <el-button
                  size="small"
                  type="primary"
                  @click="applySystemPromptOverride"
                >
                  {{ t("common.save") }}
                </el-button>
                <el-button
                  size="small"
                  @click="showSystemPromptPopover = false"
                >
                  {{ t("common.cancel") }}
                </el-button>
              </div>
            </div>
          </el-popover>
          <!-- P2-13: ⋯ more 菜单折叠次级操作（refresh / expand / ai window / rename / history） -->
          <el-dropdown
            trigger="click"
            placement="bottom-end"
            @command="handleMoreCommand"
          >
            <button
              type="button"
              class="ai-chat-panel__more"
              :aria-label="t('aiChat.moreActions')"
              :title="t('aiChat.moreActions')"
            >
              <el-icon :size="14"><More /></el-icon>
            </button>
            <template #dropdown>
              <el-dropdown-menu>
                <!-- N-50/Proposal S: Refresh model list from /v1/models endpoint -->
                <el-dropdown-item command="refresh" :disabled="refreshingModels">
                  <el-icon :class="{ 'is-loading': refreshingModels }"><Refresh /></el-icon>
                  <span>{{ t("aiChat.refreshModels") }}</span>
                </el-dropdown-item>
                <!-- Plan 11 Task 1 Step 5 — 展开为独立全屏页面 /ai -->
                <el-dropdown-item command="expand">
                  <el-icon><FullScreen /></el-icon>
                  <span>{{ t("aiChat.expandStandalone") }}</span>
                </el-dropdown-item>
                <!-- prompt-4 — 打开 OS 级 AI 伴侣窗口 -->
                <el-dropdown-item command="aiwindow">
                  <el-icon><Monitor /></el-icon>
                  <span>{{ t("aiChat.openAIWindow") }}</span>
                </el-dropdown-item>
                <el-dropdown-item command="rename">
                  <el-icon><Edit /></el-icon>
                  <span>{{ t("aiChat.rename") }}</span>
                </el-dropdown-item>
                <el-dropdown-item command="history">
                  <el-icon><Clock /></el-icon>
                  <span>{{ t("aiChat.history") }}</span>
                </el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
          <button
            type="button"
            v-if="hasMessages"
            class="ai-chat-panel__clear"
            :aria-label="t('aiChat.clearConversation')"
            :title="t('aiChat.clearConversation')"
            @click="clearMessages"
          >
            <el-icon :size="14"><Close /></el-icon>
          </button>
          <button
            type="button"
            class="ai-chat-panel__close"
            :aria-label="t('aiChat.closeAiChat')"
            :title="t('aiChat.closeAiChat')"
            @click="toggleAiChat"
          >
            <el-icon :size="14"><Close /></el-icon>
          </button>
        </div>
      </div>

      <div v-if="showHistory" class="ai-chat-panel__history-panel">
        <div class="ai-chat-panel__history-header">
          <span>{{ t("aiChat.conversations") }}</span>
          <button type="button" @click="showHistory = false" :aria-label="t('aiChat.closeHistory')">×</button>
        </div>
        <div class="ai-chat-panel__history-list">
          <div v-if="conversations.length === 0" class="ai-chat-panel__history-empty">
            {{ t("aiChat.noConversations") }}
          </div>
          <div
            v-for="conv in conversations"
            :key="conv.id"
            class="ai-chat-panel__history-item"
          >
            <button
              type="button"
              class="ai-chat-panel__history-load"
              @click="handleLoadConversation(conv.id)"
            >
              <div class="ai-chat-panel__history-title">{{ conv.title }}</div>
              <div class="ai-chat-panel__history-time">{{ formatTime(conv.created_at) }}</div>
            </button>
            <button
              type="button"
              class="ai-chat-panel__history-delete"
              :aria-label="t('aiChat.deleteConversation')"
              @click="handleDeleteConversation(conv.id)"
            >×</button>
          </div>
        </div>
      </div>

      <div v-if="hasContext" class="ai-chat-panel__context-bar">
        <span class="ai-chat-panel__context-chip">
          {{ contextLabel() }}
          <button
            type="button"
            class="ai-chat-panel__context-remove"
            :aria-label="t('aiChat.removeContext')"
            @click="clearContext"
          >×</button>
        </span>
      </div>

      <div ref="messageListRef" class="ai-chat-panel__body" @scroll.passive="onMessageBodyScroll">
        <div v-if="!hasMessages" class="ai-chat-panel__empty">
          <div class="ai-chat-panel__empty-logo" aria-hidden="true">
            <el-icon :size="28"><MagicStick /></el-icon>
          </div>
          <p class="ai-chat-panel__empty-title">{{ t("aiChat.emptyTitle") }}</p>
          <p class="ai-chat-panel__empty-subtitle">
            {{ t("aiChat.emptySubtitle") }}
          </p>
          <div class="ai-chat-panel__suggestions">
            <button
              type="button"
              class="ai-chat-panel__suggestion"
              :aria-label="t('aiChat.suggestionExplain')"
              @click="sendSuggestion(t('aiChat.suggestionExplain'))"
            >
              <el-icon :size="14" aria-hidden="true"><Document /></el-icon>
              <span>{{ t("aiChat.suggestionExplain") }}</span>
            </button>
            <button
              type="button"
              class="ai-chat-panel__suggestion"
              :aria-label="t('aiChat.suggestionRefactor')"
              @click="sendSuggestion(t('aiChat.suggestionRefactor'))"
            >
              <el-icon :size="14" aria-hidden="true"><Edit /></el-icon>
              <span>{{ t("aiChat.suggestionRefactor") }}</span>
            </button>
            <button
              type="button"
              class="ai-chat-panel__suggestion"
              :aria-label="t('aiChat.suggestionTests')"
              @click="sendSuggestion(t('aiChat.suggestionTests'))"
            >
              <el-icon :size="14" aria-hidden="true"><Check /></el-icon>
              <span>{{ t("aiChat.suggestionTests") }}</span>
            </button>
            <button
              type="button"
              class="ai-chat-panel__suggestion"
              :aria-label="t('aiChat.suggestionFix')"
              @click="sendSuggestion(t('aiChat.suggestionFix'))"
            >
              <el-icon :size="14" aria-hidden="true"><Search /></el-icon>
              <span>{{ t("aiChat.suggestionFix") }}</span>
            </button>
            <button
              type="button"
              class="ai-chat-panel__suggestion"
              :aria-label="t('aiChat.suggestionDocument')"
              @click="sendSuggestion(t('aiChat.suggestionDocument'))"
            >
              <el-icon :size="14" aria-hidden="true"><EditPen /></el-icon>
              <span>{{ t("aiChat.suggestionDocument") }}</span>
            </button>
          </div>
        </div>

        <section v-else class="ai-chat-panel__messages">
          <div v-if="aiState.error" class="ai-chat-panel__error" role="alert">
            {{ aiState.error }}
          </div>
          <div
            v-for="(msg, i) in aiState.messages"
            :key="msg.id"
            v-memo="[msg.id, msg.content, msg.role, msg.toolCalls, msg.toolResults, isAgentMode, cleanedMessageCache[i]]"
            class="ai-chat-panel__message"
            :class="'ai-chat-panel__message--' + msg.role"
            role="article"
            tabindex="0"
            @contextmenu="openMessageContextMenu($event, i, msg)"
            @keydown="onMessageKeydown($event, i, msg)"
          >
            <div class="ai-chat-panel__message-header">
              <span class="ai-chat-panel__message-role">{{ msg.role }}</span>
              <button
                type="button"
                v-if="msg.role === 'assistant' && msg.content"
                class="ai-chat-panel__copy-btn"
                :aria-label="t('aiChat.copyMessage')"
                :title="t('aiChat.copyMessage')"
                @click="handleCopy(msg.content)"
              >
                <el-icon :size="12"><CopyDocument /></el-icon>
              </button>
            </div>
            <MarkdownContent
              v-if="msg.content || !(msg.toolCalls?.length || msg.toolResults?.length)"
              class="ai-chat-panel__message-content markdown-body"
              :html="renderContent(cleanedMessageCache[i] ?? msg.content)"
              @click="handleContentClick"
            />
            <div v-if="msg.toolCalls?.length" class="ai-chat-panel__native-tools" data-testid="chat-message-tool-calls">
              <div v-for="call in msg.toolCalls" :key="call.id" class="ai-chat-panel__native-tool">
                <strong>{{ call.name }}</strong>
                <code>{{ boundedNativeToolText(call.arguments, 480) }}</code>
              </div>
            </div>
            <div v-if="msg.toolResults?.length" class="ai-chat-panel__native-tools" data-testid="chat-message-tool-results">
              <div v-for="result in msg.toolResults" :key="result.toolCallId" class="ai-chat-panel__native-tool" :class="{ 'is-error': result.isError }">
                <strong>{{ result.isError ? 'Tool error' : 'Tool result' }}</strong>
                <pre>{{ boundedNativeToolText(result.content) }}</pre>
              </div>
            </div>
          </div>

          <!-- 右键菜单：复制 / 撤回 / 删除 -->
          <ul
            v-if="messageContextMenu.visible"
            class="ai-chat-panel__ctx-menu"
            role="menu"
            :aria-label="t('aiChat.messageActions')"
            :style="{ left: messageContextMenu.x + 'px', top: messageContextMenu.y + 'px' }"
          >
            <li class="ai-chat-panel__ctx-item" role="menuitem" tabindex="0" @click="ctxCopyMessage" @keydown.enter.prevent="ctxCopyMessage" @keydown.space.prevent="ctxCopyMessage">
              <el-icon><CopyDocument /></el-icon>
              <span>{{ t("aiChat.copyMessage") }}</span>
            </li>
            <li class="ai-chat-panel__ctx-item ai-chat-panel__ctx-item--danger" role="menuitem" tabindex="0" @click="ctxRevokeFromHere" @keydown.enter.prevent="ctxRevokeFromHere" @keydown.space.prevent="ctxRevokeFromHere">
              <el-icon><Back /></el-icon>
              <span>{{ t("aiChat.revokeFromHere") }}</span>
            </li>
            <li class="ai-chat-panel__ctx-item ai-chat-panel__ctx-item--danger" role="menuitem" tabindex="0" @click="ctxDeleteMessage" @keydown.enter.prevent="ctxDeleteMessage" @keydown.space.prevent="ctxDeleteMessage">
              <el-icon><Delete /></el-icon>
              <span>{{ t("aiChat.deleteMessage") }}</span>
            </li>
          </ul>

          <!-- Agent mode: pending tool-call approvals -->
          <div
            v-if="isAgentMode && pendingToolCalls.length > 0"
            class="agent-approvals"
          >
            <div class="agent-approvals__header">
              <span>{{ t("aiChat.toolCalls", { count: pendingToolCalls.length }) }}</span>
              <button
                type="button"
                class="agent-approvals__clear"
                :disabled="agentTurnBusy"
                :aria-label="t('aiChat.clearToolCalls')"
                :title="t('aiChat.clearToolCalls')"
                @click="handleClearPendingToolCalls"
              >×</button>
            </div>
            <div v-if="hasPendingRunToolCalls" class="agent-approvals__denylist-warning">
              {{ t("aiChat.denylistWarning") }}
            </div>
            <div
              v-for="tc in pendingToolCalls"
              :key="tc.id"
              class="tool-call-card"
              :class="'tool-call-card--' + tc.status"
            >
              <div class="tool-call-card__header">
                <el-icon :size="14" class="tool-call-card__icon">
                  <component :is="toolCallIcon(tc.kind)" />
                </el-icon>
                <span class="tool-call-card__kind">{{ tc.kind }}</span>
                <code class="tool-call-card__target" :title="tc.target">{{ tc.target }}</code>
                <span
                  v-if="effectiveRiskLevel(tc)"
                  class="tool-call-card__risk"
                  :class="'tool-call-card__risk--' + effectiveRiskLevel(tc)"
                >
                  {{ riskBadgeLabel(effectiveRiskLevel(tc)) }}
                </span>
                <span class="tool-call-card__status" :class="'tool-call-card__status--' + tc.status">
                  {{ toolCallStatusLabel(tc) }}
                </span>
              </div>
              <div v-if="tc.blockReason" class="tool-call-card__block-warning">
                {{ t("aiChat.blocked", { reason: tc.blockReason }) }}
              </div>
              <div v-if="tc.kind === 'write' && tc.content" class="tool-call-card__preview">
                <pre>{{ tc.content.length > 400 ? tc.content.slice(0, 400) + '\n…' : tc.content }}</pre>
              </div>
							<div v-else-if="!['read', 'run', 'search'].includes(tc.kind) && tc.arguments" class="tool-call-card__preview">
								<pre>{{ structuredArguments(tc).length > 600 ? structuredArguments(tc).slice(0, 600) + '\n…' : structuredArguments(tc) }}</pre>
							</div>
              <div v-if="tc.result && (tc.status === 'executed' || tc.status === 'error')" class="tool-call-card__result">
                <pre>{{ tc.result.length > 600 ? tc.result.slice(0, 600) + '\n…' : tc.result }}</pre>
              </div>
              <div v-if="tc.status === 'pending'" class="tool-call-card__actions">
                <button
                  type="button"
                  class="tool-call-card__btn tool-call-card__btn--approve"
                  :disabled="agentTurnBusy || !!tc.blockReason"
                  @click="handleApprove(tc)"
                >
                  <el-icon :size="12"><Check /></el-icon>
                  {{ t("aiChat.approveAndRun") }}
                </button>
                <button
                  type="button"
                  class="tool-call-card__btn tool-call-card__btn--reject"
                  :disabled="agentTurnBusy"
                  @click="handleReject(tc)"
                >
                  <el-icon :size="12"><CloseIcon /></el-icon>
                  {{ t("aiChat.reject") }}
                </button>
              </div>
            </div>
          </div>
        </section>
        <!-- Keep execution activity visible even while the assistant message
             list is empty (for example, during the first tool-call event). -->
        <AgentExecutionTimeline />
      </div>

      <div class="ai-chat-panel__input-area">
        <div v-if="aiState.mentionedFiles.length > 0" class="chat-mentions">
          <span
            v-for="file in aiState.mentionedFiles"
            :key="file.filePath"
            class="chat-mention-chip"
          >
            {{ file.filePath.split('/').pop() }}
            <button
              type="button"
              class="chat-mention-chip__remove"
              :aria-label="t('aiChat.removeFile', { path: file.filePath })"
              @click="removeMentionedFile(file.filePath)"
            >
              ×
            </button>
          </span>
        </div>
        <div v-if="showFilePicker" class="chat-file-picker">
          <input
            v-model="manualFilePath"
            class="chat-file-picker__input"
            :placeholder="t('aiChat.filePathPlaceholder')"
            @keyup.enter="handleAddFileByPath"
          />
          <button
            type="button"
            class="chat-file-picker__add"
            @click="handleAddFileByPath"
          >
            {{ t("aiChat.add") }}
          </button>
        </div>
        <div class="ai-chat-panel__input-wrap">
          <input
            v-model="inputText"
            type="text"
            class="ai-chat-panel__input"
            :placeholder="t('aiChat.inputPlaceholder')"
            name="ai-chat-input"
            :aria-label="t('a11y.aiChatInput')"
            :disabled="aiState.streaming || aiState.globalStreamBusy"
            @keydown="handleKeydown"
          />
          <button
            type="button"
            v-if="aiState.streaming"
            class="ai-chat-panel__stop"
            :aria-label="t('aiChat.stopGeneration')"
            :title="t('aiChat.stopGeneration')"
            @click="handleStop"
          >
            <el-icon :size="14"><VideoPause /></el-icon>
          </button>
          <button
            type="button"
            v-else
            class="ai-chat-panel__send"
            :aria-label="t('aiChat.sendMessage')"
            :title="!connectivityState.online ? t('aiChat.sendDisabledOffline') : t('aiChat.sendMessage')"
            :disabled="!inputText.trim() || !connectivityState.online || aiState.globalStreamBusy"
            @click="handleSend"
          >
            <el-icon :size="14"><Promotion /></el-icon>
          </button>
          <button
            type="button"
            class="chat-input__mention-btn"
            :aria-label="t('aiChat.addFileContext')"
            :title="t('aiChat.addFileContextTitle')"
            @click="showFilePicker = !showFilePicker"
          >
            @
          </button>
        </div>
      </div>

      <!-- Side diff apply modal (#12) -->
      <transition name="fade">
          <div
            v-if="diffModalVisible"
            class="apply-diff-overlay"
          >
          <button
            type="button"
            class="dialog-backdrop-button"
            tabindex="-1"
            :aria-label="t('a11y.closeDialog')"
            @click="cancelApplyDiff"
          />
          <FocusTrapDialog
            class="apply-diff-modal"
            :aria-label="t('aiChat.applyCodeLabel')"
            @close="cancelApplyDiff"
          >
            <div class="apply-diff-modal__header">
              <span class="apply-diff-modal__title">
                {{ t("aiChat.applyToTitle", { name: diffTargetPath.split(/[/\\]/).pop() ?? diffTargetPath }) }}
              </span>
              <button
                type="button"
                class="apply-diff-modal__close"
                :aria-label="t('aiChat.closeDiffPreview')"
                @click="cancelApplyDiff"
              >
                <el-icon :size="14"><Close /></el-icon>
              </button>
            </div>
            <div class="apply-diff-modal__body">
              <VueMonacoDiffEditor
                :original="diffOriginal"
                :modified="diffModified"
                :language="diffTargetLanguage"
                :theme="diffMonacoTheme"
                :options="{
                  readOnly: true,
                  fontSize: appState.fontSize,
                  fontFamily: appState.fontFamily,
                  minimap: { enabled: false },
                  renderSideBySide: true,
                  automaticLayout: true,
                }"
                height="100%"
              />
            </div>
            <div class="apply-diff-modal__footer">
              <button
                type="button"
                class="apply-diff-modal__btn apply-diff-modal__btn--secondary"
                @click="cancelApplyDiff"
              >
                {{ t("common.cancel") }}
              </button>
              <button
                type="button"
                class="apply-diff-modal__btn apply-diff-modal__btn--primary"
                @click="confirmApplyDiff"
              >
                {{ t("aiChat.applyToFile") }}
              </button>
            </div>
          </FocusTrapDialog>
        </div>
      </transition>

      <!-- Project rules view/edit modal (#25) -->
      <transition name="fade">
          <div
            v-if="rulesModalVisible"
            class="rules-modal-overlay"
          >
          <button
            type="button"
            class="dialog-backdrop-button"
            tabindex="-1"
            :aria-label="t('a11y.closeDialog')"
            @click="cancelRulesEdit"
          />
          <FocusTrapDialog
            class="rules-modal"
            :aria-label="t('aiChat.projectRules')"
            @close="cancelRulesEdit"
          >
            <div class="rules-modal__header">
              <span class="rules-modal__title">{{ t("aiChat.projectRules") }}</span>
              <button
                type="button"
                class="rules-modal__close"
                :aria-label="t('aiChat.closeRulesEditor')"
                @click="cancelRulesEdit"
              >
                <el-icon :size="14"><Close /></el-icon>
              </button>
            </div>
            <div class="rules-modal__body">
              <div class="rules-modal__path-row">
                <label class="rules-modal__label" for="rules-path-select">{{ t("aiChat.file") }}</label>
                <el-select
                  id="rules-path-select"
                  v-model="rulesDraftPath"
                  filterable
                  allow-create
                  default-first-option
                  size="small"
                  placeholder="Select or type a path"
                  class="rules-modal__path-select"
                >
                  <el-option
                    v-for="opt in rulesCandidateOptions"
                    :key="opt.value"
                    :label="opt.label"
                    :value="opt.value"
                  />
                </el-select>
                <button
                  type="button"
                  class="rules-modal__reload"
                  :disabled="!appState.currentProject"
                  :title="t('aiChat.reload')"
                  @click="handleReloadRules"
                >{{ t("aiChat.reload") }}</button>
              </div>
              <p class="rules-modal__hint">
                Rules are appended to the AI system prompt for every conversation in this project.
                Supports Markdown. Stored at the chosen path inside the project root.
              </p>
              <textarea
                v-model="rulesDraft"
                class="rules-modal__editor"
                placeholder="# Project rules&#10;&#10;Be concise.&#10;Always use TypeScript.&#10;..."
                spellcheck="false"
              />
              <div class="rules-modal__config-section">
                <button
                  type="button"
                  class="rules-modal__config-toggle"
                  :aria-expanded="rulesConfigVisible"
                  @click="rulesConfigVisible = !rulesConfigVisible"
                >
                  {{ rulesConfigVisible ? '▾' : '▸' }} Advanced: Rules configuration (N-18)
                </button>
                <div v-if="rulesConfigVisible" class="rules-modal__config-body">
                  <p class="rules-modal__hint">
                    Configure which rule files are loaded and how they combine.
                    Built-in candidates (.koyori-ide/rules.md, .cursorrules, AGENTS.md, .ai/rules.md)
                    are always probed; add custom paths or globs (e.g. <code>docs/**/*.rules.md</code>)
                    below. Mode "first" loads only the first existing file; "merge" concatenates all.
                  </p>
                  <div class="rules-modal__config-mode">
                    <label class="rules-modal__label">Mode</label>
                    <el-radio-group v-model="rulesConfigDraft.mode" size="small">
                      <el-radio-button value="first">{{ t("aiChat.firstFileOnly") }}</el-radio-button>
                      <el-radio-button value="merge">{{ t("aiChat.mergeAllFiles") }}</el-radio-button>
                    </el-radio-group>
                    <span v-if="rulesFileCount > 0" class="rules-modal__config-status">
                      Currently: {{ rulesFileCount }} file(s) loaded
                    </span>
                  </div>
                  <div class="rules-modal__config-candidates">
                    <label class="rules-modal__label">Custom candidates</label>
                    <div
                      v-for="(c, idx) in rulesConfigDraft.candidates"
                      :key="rulesCandidateKey(c)"
                      class="rules-modal__config-row"
                    >
                      <el-input
                        v-model="c.path"
                        size="small"
                        placeholder="path or glob (e.g. CLAUDE.md)"
                        class="rules-modal__config-path"
                      />
                      <el-input
                        v-model="c.source"
                        size="small"
                        placeholder="source label"
                        class="rules-modal__config-source"
                      />
                      <button
                        type="button"
                        class="rules-modal__config-remove"
                        :aria-label="`Remove candidate ${idx + 1}`"
                        @click="removeRulesCandidate(idx)"
                      >
                        <el-icon :size="12"><Close /></el-icon>
                      </button>
                    </div>
                    <button type="button" class="rules-modal__config-add" @click="addRulesCandidate">
                      {{ t("aiChat.addCandidate") }}
                    </button>
                  </div>
                  <div class="rules-modal__config-actions">
                    <button
                      type="button"
                      class="rules-modal__btn rules-modal__btn--primary rules-modal__btn--small"
                      :disabled="rulesConfigSaving || !appState.currentProject"
                      @click="handleSaveRulesConfig"
                    >
                      {{ rulesConfigSaving ? t('aiChat.saving') : t('aiChat.saveConfig') }}
                    </button>
                  </div>
                </div>
              </div>
            </div>
            <div class="rules-modal__footer">
              <button
                type="button"
                class="rules-modal__btn rules-modal__btn--secondary"
                @click="cancelRulesEdit"
              >
                {{ t("common.cancel") }}
              </button>
              <button
                type="button"
                class="rules-modal__btn rules-modal__btn--primary"
                :disabled="rulesSaving || !appState.currentProject"
                @click="handleSaveRules"
              >
                {{ rulesSaving ? t('aiChat.saving') : t('aiChat.saveRules') }}
              </button>
            </div>
          </FocusTrapDialog>
        </div>
      </transition>
    </aside>
  </transition>
</template>

<style scoped>
/* ═══════════════════════════════════════════════════════════════════════════
   AiChatPanel — Apple Design Language (DESIGN.md)
   - 单一 Action Blue 交互色，无渐变、无装饰阴影
   - SF Pro Text 17px body / SF Pro Display 标题 / 负字距
   - pill CTA + 8px utility rect + 44px 触控目标
   - 表面通过 canvas / parchment / surface-tile 区分，不靠阴影
   - scale(0.95) 按压反馈是唯一的微交互
   ═══════════════════════════════════════════════════════════════════════════ */

.ai-chat-panel {
  display: flex;
  flex-direction: column;
  min-width: 0;
  height: 100%;
  /* 画布：parchment 让面板与侧栏其余部分形成微步层级 */
  background-color: var(--color-canvas-parchment);
  overflow: hidden;
  flex-shrink: 0;
  z-index: 5;
  position: relative;
}

.ai-chat-panel--embedded {
  flex: 1;
  width: 100% !important;
  z-index: 1;
  border-top: 1px solid var(--color-hairline);
}

/* ── Header：44px 高、画布背景、hairline 底边 ── */
.ai-chat-panel__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 var(--space-sm);
  height: 44px;
  min-height: 44px;
  background-color: var(--color-canvas);
  border-bottom: 1px solid var(--color-hairline);
}

.ai-chat-panel__header-left {
  display: flex;
  align-items: center;
  gap: var(--space-xs);
  color: var(--color-text-tertiary);
}

.ai-chat-panel__title {
  /* caption-strong: 14px / 600 / -0.224px */
  font-family: var(--font-sans);
  font-size: 14px;
  font-weight: 600;
  line-height: 1.29;
  letter-spacing: -0.224px;
  color: var(--color-text-primary);
  text-transform: none;
}

.ai-chat-panel__header-right {
  display: flex;
  align-items: center;
  gap: var(--space-xxs);
}

.ai-chat-panel__config-select {
  width: 120px;
}

.ai-chat-panel__config-select :deep(.el-input__wrapper) {
  background-color: transparent;
  box-shadow: 0 0 0 1px var(--color-hairline) inset;
  border-radius: var(--radius-pill);
  min-height: 26px;
}

.ai-chat-panel__config-select :deep(.el-input__inner) {
  font-size: 12px;
  color: var(--chrome-text-secondary);
}

.ai-chat-panel__model-select {
  width: 140px;
}

.ai-chat-panel__model-select :deep(.el-input__wrapper) {
  background-color: transparent;
  box-shadow: 0 0 0 1px var(--color-hairline) inset;
  border-radius: var(--radius-pill);
  min-height: 26px;
  padding: 0 var(--space-xs);
}

.ai-chat-panel__model-select :deep(.el-input__inner) {
  font-family: var(--font-sans);
  font-size: 12px;
  line-height: 1.0;
  letter-spacing: -0.12px;
  color: var(--color-text-secondary);
  height: 26px;
  line-height: 26px;
}

.ai-chat-panel__model-select :deep(.el-input__inner::placeholder) {
  color: var(--color-text-tertiary);
}

/* ── Apple button-icon-circular：44×44 圆形、translucent chip 灰 ──
   这里用 28×28 紧凑变体以适配 44px header；触控目标通过 padding 扩展。 */
.ai-chat-panel__refresh-models,
.ai-chat-panel__clear,
.ai-chat-panel__close,
.ai-chat-panel__history,
.ai-chat-panel__sysprompt,
.ai-chat-panel__more {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  padding: 0;
  border: none;
  background: transparent;
  color: var(--color-text-tertiary);
  cursor: pointer;
  border-radius: var(--radius-full);
  transition: color var(--transition-fast), background-color var(--transition-fast), transform var(--transition-fast);
}

.ai-chat-panel__refresh-models:hover:not(:disabled),
.ai-chat-panel__clear:hover,
.ai-chat-panel__close:hover,
.ai-chat-panel__history:hover,
.ai-chat-panel__sysprompt:hover,
.ai-chat-panel__more:hover {
  color: var(--color-text-primary);
  background-color: var(--color-bg-surface-container-high);
}

.ai-chat-panel__refresh-models:active:not(:disabled),
.ai-chat-panel__clear:active,
.ai-chat-panel__close:active,
.ai-chat-panel__history:active,
.ai-chat-panel__sysprompt:active,
.ai-chat-panel__more:active {
  transform: scale(0.95);
}

.ai-chat-panel__refresh-models:disabled {
  cursor: wait;
  opacity: 0.5;
}

.ai-chat-panel__refresh-models .is-loading {
  animation: ai-chat-panel-spin 1s linear infinite;
}

@keyframes ai-chat-panel-spin {
  from { transform: rotate(0deg); }
  to { transform: rotate(360deg); }
}

/* P2-13: ⋯ more 菜单项图标 + 文字间距与旋转动画 */
.ai-chat-panel__more :deep(.is-loading) {
  animation: ai-chat-panel-spin 1s linear infinite;
}

.ai-chat-panel__header-right :deep(.el-dropdown-menu__item .el-icon) {
  margin-right: 8px;
}

/* ── 模式切换 / Rules 徽章：Apple pill 配置器 chip ── */
.ai-chat-panel__mode-toggle,
.ai-chat-panel__rules-badge {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 4px 10px;
  font-family: var(--font-sans);
  font-size: 11px;
  font-weight: 400;
  line-height: 1.0;
  letter-spacing: -0.12px;
  color: var(--color-text-tertiary);
  background: var(--color-canvas);
  border: 1px solid var(--color-hairline);
  border-radius: var(--radius-pill);
  cursor: pointer;
  transition: color var(--transition-fast), background-color var(--transition-fast), border-color var(--transition-fast), transform var(--transition-fast);
}

.ai-chat-panel__mode-toggle:hover,
.ai-chat-panel__rules-badge:hover {
  color: var(--color-text-primary);
  border-color: var(--color-text-tertiary);
}

.ai-chat-panel__mode-toggle:active,
.ai-chat-panel__rules-badge:active {
  transform: scale(0.95);
}

.ai-chat-panel__mode-toggle--active,
.ai-chat-panel__rules-badge--active {
  color: var(--color-primary);
  background-color: color-mix(in srgb, var(--color-primary) 8%, transparent);
  border-color: var(--color-primary);
}

.ai-chat-panel__mode-toggle--active:hover,
.ai-chat-panel__rules-badge--active:hover {
  color: var(--color-primary);
  background-color: color-mix(in srgb, var(--color-primary) 14%, transparent);
}

.ai-chat-panel__sysprompt--active {
  color: var(--color-primary);
}

/* ── Context bar ── */
.ai-chat-panel__context-bar {
  padding: var(--space-xxs) var(--space-sm);
  border-bottom: 1px solid var(--color-hairline);
  background-color: var(--color-canvas);
}

.ai-chat-panel__context-chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 3px 10px;
  font-family: var(--font-sans);
  font-size: 11px;
  line-height: 1.0;
  letter-spacing: -0.12px;
  color: var(--color-text-secondary);
  background-color: var(--color-bg-surface-container);
  border-radius: var(--radius-pill);
}

.ai-chat-panel__context-remove {
  border: none;
  background: transparent;
  color: var(--color-text-tertiary);
  cursor: pointer;
  font-size: 14px;
  line-height: 1;
  padding: 0;
}

.ai-chat-panel__context-remove:hover {
  color: var(--color-text-primary);
}

/* ── 消息列表区 ── */
.ai-chat-panel__body {
  flex: 1;
  overflow-y: auto;
  padding: 0;
  background-color: var(--color-canvas-parchment);
}

/* ── Empty 状态：Apple 产品 tile 美学 —— 大标题、tagline、pill CTA ── */
.ai-chat-panel__empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  height: 100%;
  padding: var(--space-xl) var(--space-md);
  text-align: center;
}

.ai-chat-panel__empty-logo {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 56px;
  height: 56px;
  border-radius: var(--radius-full);
  /* Apple 单一 Action Blue，无渐变 */
  background-color: var(--color-primary);
  color: var(--color-on-primary);
  margin-bottom: var(--space-md);
  flex-shrink: 0;
}

.ai-chat-panel__empty-title {
  /* display-md: 34px / 600 / -0.374px —— Apple "tight" 标题 */
  font-family: var(--font-display);
  font-size: 28px;
  font-weight: 600;
  line-height: 1.10;
  letter-spacing: -0.28px;
  color: var(--color-text-primary);
  margin-bottom: var(--space-xs);
}

.ai-chat-panel__empty-subtitle {
  /* lead: 28px / 400 —— tagline 副标题 */
  font-family: var(--font-display);
  font-size: 17px;
  font-weight: 400;
  line-height: 1.47;
  letter-spacing: -0.374px;
  color: var(--color-text-tertiary);
  max-width: 320px;
  margin-bottom: var(--space-lg);
}

.ai-chat-panel__suggestions {
  display: flex;
  flex-direction: column;
  gap: var(--space-xs);
  width: 100%;
  max-width: 300px;
}

/* Apple configurator-option-chip：pill 形、hairline 边框、scale 按压 */
.ai-chat-panel__suggestion {
  display: flex;
  align-items: center;
  gap: var(--space-xs);
  padding: 10px var(--space-sm);
  font-family: var(--font-sans);
  font-size: 14px;
  font-weight: 400;
  line-height: 1.29;
  letter-spacing: -0.224px;
  color: var(--color-text-secondary);
  background: var(--color-canvas);
  border: 1px solid var(--color-hairline);
  border-radius: var(--radius-pill);
  cursor: pointer;
  text-align: left;
  transition: color var(--transition-fast), border-color var(--transition-fast), background-color var(--transition-fast), transform var(--transition-fast);
}

.ai-chat-panel__suggestion:hover {
  color: var(--color-primary);
  border-color: var(--color-primary);
  background: color-mix(in srgb, var(--color-primary) 4%, transparent);
}

.ai-chat-panel__suggestion:active {
  transform: scale(0.95);
}

.ai-chat-panel__suggestion .el-icon {
  color: var(--color-text-tertiary);
  flex-shrink: 0;
}

.ai-chat-panel__suggestion:hover .el-icon {
  color: var(--color-primary);
}

/* ── 消息气泡：通过 surface-color 区分，无阴影、发丝级边框 ── */
.ai-chat-panel__messages {
  display: flex;
  flex-direction: column;
  gap: var(--space-sm);
  padding: var(--space-sm);
}

.ai-chat-panel__message {
  padding: var(--space-sm) var(--space-md);
  border-radius: var(--radius-lg);
  /* Apple body: 17px / 400 / 1.47 / -0.374px */
  font-family: var(--font-sans);
  font-size: 15px;
  font-weight: 400;
  line-height: 1.47;
  letter-spacing: -0.2px;
}

.ai-chat-panel__message--user {
  background-color: var(--color-canvas);
  color: var(--color-text-primary);
  border: 1px solid var(--color-hairline);
}

.ai-chat-panel__message--assistant {
  /* 暗色 tile 等价的微步层级：用 surface-tile 概念在亮色下用 parchment */
  background-color: var(--color-canvas);
  color: var(--color-text-primary);
  border: 1px solid var(--color-hairline);
}

.ai-chat-panel__message-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 4px;
}

.ai-chat-panel__message-role {
  /* fine-print: 12px / 400 / -0.12px */
  font-family: var(--font-sans);
  font-size: 12px;
  font-weight: 400;
  line-height: 1.0;
  letter-spacing: -0.12px;
  color: var(--color-text-tertiary);
  text-transform: capitalize;
}

.ai-chat-panel__copy-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  border: none;
  background: transparent;
  color: var(--color-text-tertiary);
  cursor: pointer;
  border-radius: var(--radius-full);
  opacity: 0;
  transition: opacity var(--transition-fast), color var(--transition-fast), background-color var(--transition-fast);
}

.ai-chat-panel__message:hover .ai-chat-panel__copy-btn {
  opacity: 1;
}

.ai-chat-panel__copy-btn:hover {
  color: var(--color-text-primary);
  background-color: var(--color-bg-surface-container-high);
}

.ai-chat-panel__message-content {
  word-wrap: break-word;
}

.ai-chat-panel__message-content :deep(pre) {
  margin: var(--space-xs) 0;
  padding: var(--space-sm) var(--space-md);
  background-color: var(--hljs-bg, var(--color-bg-surface-container-low));
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-sm);
  overflow-x: auto;
  font-size: 13px;
  line-height: 1.5;
}

.ai-chat-panel__message-content :deep(code) {
  font-family: var(--font-mono);
  font-size: 13px;
}

.ai-chat-panel__message-content :deep(code.hljs) {
  background: transparent;
  padding: 0;
  font-weight: 500;
}

.ai-chat-panel__message-content :deep(.code-block-wrap) {
  position: relative;
  margin: var(--space-xs) 0;
}

.ai-chat-panel__message-content :deep(.code-block-wrap > pre) {
  margin: 0;
  border-top-right-radius: 0;
}

.ai-chat-panel__message-content :deep(.code-block-apply-btn) {
  position: absolute;
  top: var(--space-xxs);
  right: var(--space-xxs);
  padding: 3px 10px;
  font-family: var(--font-sans);
  font-size: 11px;
  line-height: 1.0;
  letter-spacing: -0.12px;
  color: var(--color-primary);
  background-color: var(--color-canvas);
  border: 1px solid var(--color-hairline);
  border-radius: var(--radius-pill);
  cursor: pointer;
  opacity: 0;
  transition: opacity var(--transition-fast), color var(--transition-fast), background-color var(--transition-fast), border-color var(--transition-fast);
}

.ai-chat-panel__message-content :deep(.code-block-wrap:hover .code-block-apply-btn) {
  opacity: 1;
}

.ai-chat-panel__message-content :deep(.code-block-apply-btn:hover) {
  color: var(--color-on-primary);
  background-color: var(--color-primary);
  border-color: var(--color-primary);
}

.ai-chat-panel__message-content :deep(p) {
  margin: var(--space-xxs) 0;
}

.ai-chat-panel__message-content :deep(ul),
.ai-chat-panel__message-content :deep(ol) {
  margin: var(--space-xxs) 0;
  padding-left: var(--space-md);
}

.ai-chat-panel__error {
  padding: var(--space-xs) var(--space-sm);
  font-family: var(--font-sans);
  font-size: 13px;
  line-height: 1.43;
  letter-spacing: -0.224px;
  color: var(--color-error);
  background-color: var(--color-error-container);
  border-radius: var(--radius-sm);
}

/* ── Input area：Apple search-input (pill, 44px) + 圆形 Action Blue 发送 ── */
.ai-chat-panel__input-area {
  padding: var(--space-xs) var(--space-sm) var(--space-sm);
  background-color: var(--color-canvas);
  border-top: 1px solid var(--color-hairline);
}

.ai-chat-panel__input-wrap {
  display: flex;
  align-items: center;
  gap: var(--space-xs);
  padding: 8px 8px 8px var(--space-md);
  background-color: var(--color-bg-surface-container-low);
  border: 1px solid var(--color-hairline);
  border-radius: var(--radius-pill);
  transition: border-color var(--transition-fast), box-shadow var(--transition-fast);
  min-height: 44px;
}

.ai-chat-panel__input-wrap:focus-within {
  border-color: var(--color-primary-focus);
  box-shadow: 0 0 0 2px color-mix(in srgb, var(--color-primary-focus) 25%, transparent);
}

.ai-chat-panel__input {
  flex: 1;
  min-width: 0;
  padding: 6px 0;
  /* Apple body: 17px / 400 / -0.374px */
  font-family: var(--font-sans);
  font-size: 15px;
  font-weight: 400;
  line-height: 1.47;
  letter-spacing: -0.2px;
  color: var(--color-text-primary);
  background: transparent;
  border: none;
  outline: none;
}

.ai-chat-panel__input::placeholder {
  color: var(--color-text-tertiary);
}

.ai-chat-panel__input:disabled {
  opacity: 0.5;
}

/* Apple button-primary：Action Blue pill + scale(0.95) 按压 */
.ai-chat-panel__send,
.ai-chat-panel__stop {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  border: none;
  border-radius: var(--radius-full);
  color: var(--color-on-primary);
  cursor: pointer;
  flex-shrink: 0;
  transition: background-color var(--transition-fast), transform var(--transition-fast);
}

.ai-chat-panel__send {
  background-color: var(--color-primary);
}

.ai-chat-panel__send:hover:not(:disabled) {
  background-color: var(--color-primary-focus);
}

.ai-chat-panel__send:active:not(:disabled) {
  transform: scale(0.95);
}

.ai-chat-panel__send:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.ai-chat-panel__stop {
  background-color: var(--color-error);
}

.ai-chat-panel__stop:hover {
  background-color: color-mix(in srgb, var(--color-error) 85%, #000);
}

.ai-chat-panel__stop:active {
  transform: scale(0.95);
}

/* @ mention 按钮：configurator-option-chip 风格 */
.chat-input__mention-btn {
  background: none;
  border: 1px solid var(--color-hairline);
  border-radius: var(--radius-pill);
  color: var(--color-text-secondary);
  cursor: pointer;
  font-family: var(--font-sans);
  font-size: 13px;
  font-weight: 600;
  padding: 4px 10px;
  transition: color var(--transition-fast), background-color var(--transition-fast), border-color var(--transition-fast);
}

.chat-input__mention-btn:hover {
  color: var(--color-primary);
  border-color: var(--color-primary);
  background: color-mix(in srgb, var(--color-primary) 4%, transparent);
}

/* ── Mention chips ── */
.chat-mentions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-xxs);
  padding: var(--space-xxs) var(--space-xs);
  border-bottom: 1px solid var(--color-hairline);
}

.chat-mention-chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 3px 10px;
  background: var(--color-canvas);
  border: 1px solid var(--color-hairline);
  border-radius: var(--radius-pill);
  font-family: var(--font-sans);
  font-size: 11px;
  line-height: 1.0;
  letter-spacing: -0.12px;
  color: var(--color-text-secondary);
}

.chat-mention-chip__remove {
  background: none;
  border: none;
  color: var(--color-text-tertiary);
  cursor: pointer;
  font-size: 14px;
  line-height: 1;
  padding: 0;
}

.chat-mention-chip__remove:hover {
  color: var(--color-text-primary);
}

/* ── File picker ── */
.chat-file-picker {
  display: flex;
  gap: var(--space-xxs);
  padding: var(--space-xxs) var(--space-xs);
  border-bottom: 1px solid var(--color-hairline);
}

.chat-file-picker__input {
  flex: 1;
  background: var(--color-canvas);
  border: 1px solid var(--color-hairline);
  border-radius: var(--radius-pill);
  color: var(--color-text-primary);
  font-family: var(--font-sans);
  font-size: 12px;
  padding: 6px var(--space-sm);
  outline: none;
  transition: border-color var(--transition-fast);
}

.chat-file-picker__input:focus {
  border-color: var(--color-primary-focus);
}

.chat-file-picker__add {
  background: var(--color-primary);
  border: none;
  border-radius: var(--radius-pill);
  color: var(--color-on-primary);
  cursor: pointer;
  font-family: var(--font-sans);
  font-size: 12px;
  padding: 6px var(--space-sm);
  transition: background-color var(--transition-fast), transform var(--transition-fast);
}

.chat-file-picker__add:hover {
  background-color: var(--color-primary-focus);
}

.chat-file-picker__add:active {
  transform: scale(0.95);
}

/* ── History panel ── */
.ai-chat-panel__history-panel {
  position: absolute;
  top: 44px;
  right: 0;
  width: 100%;
  max-height: 320px;
  background-color: var(--color-canvas);
  border-bottom: 1px solid var(--color-hairline);
  overflow-y: auto;
  z-index: 10;
}

.ai-chat-panel__history-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: var(--space-xs) var(--space-sm);
  font-family: var(--font-sans);
  font-size: 12px;
  font-weight: 600;
  line-height: 1.0;
  letter-spacing: -0.12px;
  color: var(--color-text-tertiary);
  text-transform: none;
}

.ai-chat-panel__history-list {
  padding: 0 0 var(--space-xs);
}

.ai-chat-panel__history-empty {
  padding: var(--space-md) var(--space-sm);
  font-family: var(--font-sans);
  font-size: 13px;
  line-height: 1.43;
  letter-spacing: -0.224px;
  color: var(--color-text-tertiary);
  text-align: center;
}

.ai-chat-panel__history-item {
  display: flex;
  align-items: center;
  padding: 0 var(--space-xxs) 0 0;
}

.ai-chat-panel__history-load {
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: var(--space-xs) var(--space-sm);
  background: transparent;
  border: none;
  cursor: pointer;
  text-align: left;
  transition: background-color var(--transition-fast);
}

.ai-chat-panel__history-load:hover {
  background-color: var(--color-bg-surface-container);
}

.ai-chat-panel__history-title {
  font-family: var(--font-sans);
  font-size: 13px;
  line-height: 1.29;
  letter-spacing: -0.224px;
  color: var(--color-text-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.ai-chat-panel__history-time {
  font-family: var(--font-sans);
  font-size: 11px;
  line-height: 1.0;
  letter-spacing: -0.12px;
  color: var(--color-text-tertiary);
}

.ai-chat-panel__history-delete {
  width: 24px;
  height: 24px;
  border: none;
  background: transparent;
  color: var(--color-text-tertiary);
  cursor: pointer;
  border-radius: var(--radius-full);
  transition: color var(--transition-fast), background-color var(--transition-fast);
}

.ai-chat-panel__history-delete:hover {
  color: var(--color-error);
  background-color: var(--color-error-container);
}

/* ── System prompt popover ── */
.ai-chat-panel__sysprompt-popover {
  display: flex;
  flex-direction: column;
  gap: var(--space-xs);
}

.ai-chat-panel__sysprompt-hint {
  margin: 0;
  font-family: var(--font-sans);
  font-size: 13px;
  line-height: 1.43;
  letter-spacing: -0.224px;
  color: var(--color-text-secondary);
}

.ai-chat-panel__sysprompt-textarea {
  width: 100%;
  resize: vertical;
  border: 1px solid var(--color-hairline);
  border-radius: var(--radius-sm);
  padding: var(--space-xs) var(--space-sm);
  font-family: var(--font-mono);
  font-size: 13px;
  line-height: 1.5;
  background: var(--color-canvas);
  color: var(--color-text-primary);
  outline: none;
  transition: border-color var(--transition-fast);
}

.ai-chat-panel__sysprompt-textarea:focus {
  border-color: var(--color-primary-focus);
}

.ai-chat-panel__sysprompt-actions {
  display: flex;
  justify-content: flex-end;
  gap: var(--space-xs);
}

/* ── Transitions ── */
.slide-chat-enter-active,
.slide-chat-leave-active {
  transition: width var(--transition-normal), opacity var(--transition-fast);
  overflow: hidden;
}

.slide-chat-enter-from,
.slide-chat-leave-to {
  width: 0;
  opacity: 0;
}

.fade-enter-active,
.fade-leave-active {
  transition: opacity var(--transition-fast);
}

.fade-enter-from,
.fade-leave-to {
  opacity: 0;
}

@media (prefers-reduced-motion: reduce) {
  .ai-chat-panel { transition: none; }
  .slide-chat-enter-active,
  .slide-chat-leave-active,
  .fade-enter-active,
  .fade-leave-active { transition: none; }
}

/* ═══════════════════════════════════════════════════════════════════════════
   Agent 工具调用卡片 — hairline 边框、无阴影、pill 操作按钮
   ═══════════════════════════════════════════════════════════════════════════ */
.agent-approvals {
  display: flex;
  flex-direction: column;
  gap: var(--space-xs);
  padding: var(--space-xs) var(--space-sm);
  margin-top: var(--space-xxs);
  border: 1px solid var(--color-hairline);
  border-radius: var(--radius-lg);
  background-color: var(--color-canvas);
}

.agent-approvals__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  font-family: var(--font-sans);
  font-size: 12px;
  font-weight: 600;
  line-height: 1.0;
  letter-spacing: -0.12px;
  color: var(--color-text-tertiary);
  text-transform: none;
}

.agent-approvals__clear {
  width: 24px;
  height: 24px;
  border: none;
  background: transparent;
  color: var(--color-text-tertiary);
  cursor: pointer;
  border-radius: var(--radius-full);
  transition: color var(--transition-fast), background-color var(--transition-fast);
}

.agent-approvals__clear:hover {
  color: var(--color-text-primary);
  background-color: var(--color-bg-surface-container-high);
}

.agent-approvals__clear:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}

/* G-SEC-02: denylist warning banner — always visible when run tool calls
   are pending, reminding the user that the denylist is not a security
   boundary and all commands require manual approval. */
.agent-approvals__denylist-warning {
  padding: var(--space-xxs) var(--space-xs);
  font-family: var(--font-sans);
  font-size: 11px;
  line-height: 1.43;
  letter-spacing: -0.224px;
  color: var(--color-warning);
  background: var(--color-warning-container);
  border-radius: var(--radius-sm);
  border-left: 2px solid var(--color-warning);
}

.tool-call-card {
  display: flex;
  flex-direction: column;
  gap: var(--space-xs);
  padding: var(--space-xs) var(--space-sm);
  background-color: var(--color-bg-surface-container-low);
  border: 1px solid var(--color-hairline);
  border-radius: var(--radius-sm);
  font-family: var(--font-sans);
  font-size: 13px;
  line-height: 1.43;
  letter-spacing: -0.224px;
}

.tool-call-card--executed {
  border-color: color-mix(in srgb, var(--color-primary) 40%, var(--color-hairline));
}

.tool-call-card--error {
  border-color: color-mix(in srgb, var(--color-error) 40%, var(--color-hairline));
}

.tool-call-card--rejected {
  opacity: 0.65;
}

.tool-call-card__header {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}

.tool-call-card__icon {
  color: var(--color-text-tertiary);
  flex-shrink: 0;
}

.tool-call-card__kind {
  font-size: 11px;
  font-weight: 600;
  line-height: 1.0;
  letter-spacing: -0.12px;
  color: var(--color-text-secondary);
  flex-shrink: 0;
  text-transform: capitalize;
}

.tool-call-card__target {
  flex: 1;
  min-width: 0;
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--color-text-primary);
  background: var(--color-canvas);
  padding: 2px var(--space-xs);
  border-radius: var(--radius-sm);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tool-call-card__status {
  font-size: 10px;
  font-weight: 400;
  line-height: 1.0;
  letter-spacing: -0.1px;
  padding: 3px var(--space-xs);
  border-radius: var(--radius-pill);
  color: var(--color-text-tertiary);
  background: var(--color-bg-surface-container-high);
  flex-shrink: 0;
}

.tool-call-card__status--pending {
  color: var(--color-warning);
  background: var(--color-warning-container);
}

.tool-call-card__status--executed {
  color: var(--color-primary);
  background: color-mix(in srgb, var(--color-primary) 10%, transparent);
}

.tool-call-card__status--error {
  color: var(--color-error);
  background: var(--color-error-container);
}

.tool-call-card__status--rejected {
  color: var(--color-text-tertiary);
}

.tool-call-card__risk {
  font-size: 10px;
  font-weight: 600;
  line-height: 1.0;
  letter-spacing: -0.1px;
  padding: 3px var(--space-xs);
  border-radius: var(--radius-pill);
  flex-shrink: 0;
}

.tool-call-card__risk--safe {
  color: var(--color-success);
  background: var(--color-success-container);
}

.tool-call-card__risk--elevated {
  color: var(--color-warning);
  background: var(--color-warning-container);
}

.tool-call-card__risk--dangerous {
  color: var(--color-error);
  background: var(--color-error-container);
}

.tool-call-card__block-warning {
  padding: var(--space-xxs) var(--space-xs);
  margin-top: var(--space-xxs);
  font-size: 11px;
  line-height: 1.43;
  letter-spacing: -0.224px;
  color: var(--color-error);
  background: var(--color-error-container);
  border-radius: var(--radius-sm);
  border-left: 2px solid var(--color-error);
}

.tool-call-card__preview pre,
.tool-call-card__result pre {
  margin: 0;
  padding: var(--space-xs) var(--space-sm);
  font-family: var(--font-mono);
  font-size: 11px;
  line-height: 1.45;
  color: var(--color-text-secondary);
  background: var(--color-canvas);
  border-radius: var(--radius-xs);
  overflow-x: auto;
  max-height: 200px;
  overflow-y: auto;
}

.tool-call-card__result pre {
  color: var(--color-text-tertiary);
}

.tool-call-card__actions {
  display: flex;
  gap: var(--space-xs);
}

/* Apple button-primary (approve) + button-secondary-pill (reject) */
.tool-call-card__btn {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 6px var(--space-sm);
  font-family: var(--font-sans);
  font-size: 13px;
  font-weight: 400;
  line-height: 1.29;
  letter-spacing: -0.224px;
  border-radius: var(--radius-pill);
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), transform var(--transition-fast);
}

.tool-call-card__btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.tool-call-card__btn:active:not(:disabled) {
  transform: scale(0.95);
}

.tool-call-card__btn--approve {
  color: var(--color-on-primary);
  background: var(--color-primary);
  border: 1px solid var(--color-primary);
}

.tool-call-card__btn--approve:hover:not(:disabled) {
  background: var(--color-primary-focus);
  border-color: var(--color-primary-focus);
}

.tool-call-card__btn--reject {
  color: var(--color-primary);
  background: transparent;
  border: 1px solid var(--color-primary);
}

.tool-call-card__btn--reject:hover:not(:disabled) {
  color: var(--color-error);
  border-color: var(--color-error);
  background: var(--color-error-container);
}

/* ═══════════════════════════════════════════════════════════════════════════
   Modals — store-utility-card 美学：18px radius、hairline 边框、无阴影
   ═══════════════════════════════════════════════════════════════════════════ */
.apply-diff-overlay,
.rules-modal-overlay {
  position: fixed;
  inset: 0;
  background-color: color-mix(in srgb, var(--color-surface-black) 50%, transparent);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 2000;
  padding: var(--space-lg);
}

.dialog-backdrop-button {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  margin: 0;
  padding: 0;
  border: 0;
  background: transparent;
  cursor: default;
}

.apply-diff-modal,
.rules-modal {
  position: relative;
  z-index: 1;
  display: flex;
  flex-direction: column;
  background-color: var(--color-canvas);
  border: 1px solid var(--color-hairline);
  border-radius: var(--radius-lg);
  overflow: hidden;
}

.apply-diff-modal {
  width: min(900px, 95vw);
  height: min(640px, 88vh);
}

.rules-modal {
  width: min(640px, 95vw);
  height: min(560px, 88vh);
}

.apply-diff-modal__header,
.rules-modal__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: var(--space-sm) var(--space-md);
  border-bottom: 1px solid var(--color-hairline);
  background-color: var(--color-canvas-parchment);
}

.apply-diff-modal__title {
  font-family: var(--font-sans);
  font-size: 14px;
  font-weight: 600;
  line-height: 1.29;
  letter-spacing: -0.224px;
  color: var(--color-text-primary);
}

.rules-modal__title {
  font-family: var(--font-sans);
  font-size: 14px;
  font-weight: 600;
  line-height: 1.29;
  letter-spacing: -0.224px;
  color: var(--color-text-primary);
  text-transform: none;
}

.apply-diff-modal__close,
.rules-modal__close {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border: none;
  border-radius: var(--radius-full);
  background: transparent;
  color: var(--color-text-tertiary);
  cursor: pointer;
  transition: color var(--transition-fast), background-color var(--transition-fast), transform var(--transition-fast);
}

.apply-diff-modal__close:hover,
.rules-modal__close:hover {
  color: var(--color-text-primary);
  background-color: var(--color-bg-surface-container-high);
}

.apply-diff-modal__close:active,
.rules-modal__close:active {
  transform: scale(0.95);
}

.apply-diff-modal__body {
  flex: 1;
  min-height: 0;
}

.apply-diff-modal__footer,
.rules-modal__footer {
  display: flex;
  justify-content: flex-end;
  gap: var(--space-xs);
  padding: var(--space-sm) var(--space-md);
  border-top: 1px solid var(--color-hairline);
  background-color: var(--color-canvas-parchment);
}

.apply-diff-modal__btn,
.rules-modal__btn {
  padding: 8px var(--space-md);
  font-family: var(--font-sans);
  font-size: 14px;
  font-weight: 400;
  line-height: 1.29;
  letter-spacing: -0.224px;
  border-radius: var(--radius-pill);
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), transform var(--transition-fast);
}

.apply-diff-modal__btn:active,
.rules-modal__btn:active {
  transform: scale(0.95);
}

.apply-diff-modal__btn--secondary,
.rules-modal__btn--secondary {
  background: transparent;
  color: var(--color-primary);
  border: 1px solid var(--color-primary);
}

.apply-diff-modal__btn--secondary:hover,
.rules-modal__btn--secondary:hover {
  color: var(--color-on-primary);
  background: var(--color-primary);
}

.apply-diff-modal__btn--primary,
.rules-modal__btn--primary {
  background: var(--color-primary);
  color: var(--color-on-primary);
  border: 1px solid var(--color-primary);
}

.apply-diff-modal__btn--primary:hover,
.rules-modal__btn--primary:hover:not(:disabled) {
  background: var(--color-primary-focus);
  border-color: var(--color-primary-focus);
}

.apply-diff-modal__btn--primary:disabled,
.rules-modal__btn--primary:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

/* ── Rules modal body ── */
.rules-modal__body {
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: var(--space-sm);
  padding: var(--space-sm) var(--space-md);
  min-height: 0;
}

.rules-modal__path-row {
  display: flex;
  align-items: center;
  gap: var(--space-xs);
}

.rules-modal__label {
  font-family: var(--font-sans);
  font-size: 12px;
  font-weight: 600;
  line-height: 1.0;
  letter-spacing: -0.12px;
  color: var(--color-text-tertiary);
  flex-shrink: 0;
  text-transform: none;
}

.rules-modal__path-select {
  flex: 1;
  min-width: 0;
}

.rules-modal__reload {
  padding: 6px var(--space-sm);
  font-family: var(--font-sans);
  font-size: 13px;
  line-height: 1.29;
  letter-spacing: -0.224px;
  color: var(--color-primary);
  background: transparent;
  border: 1px solid var(--color-primary);
  border-radius: var(--radius-pill);
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast);
  flex-shrink: 0;
}

.rules-modal__reload:hover:not(:disabled) {
  color: var(--color-on-primary);
  background: var(--color-primary);
}

.rules-modal__reload:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.rules-modal__hint {
  margin: 0;
  font-family: var(--font-sans);
  font-size: 13px;
  line-height: 1.43;
  letter-spacing: -0.224px;
  color: var(--color-text-tertiary);
}

.rules-modal__editor {
  flex: 1;
  min-height: 0;
  resize: none;
  padding: var(--space-sm);
  font-family: var(--font-mono);
  font-size: 13px;
  line-height: 1.5;
  color: var(--color-text-primary);
  background-color: var(--color-bg-surface-container-low);
  border: 1px solid var(--color-hairline);
  border-radius: var(--radius-sm);
  outline: none;
  transition: border-color var(--transition-fast);
}

.rules-modal__editor:focus {
  border-color: var(--color-primary-focus);
}

.rules-modal__editor::placeholder {
  color: var(--color-text-tertiary);
}

/* Rules config (N-18) */
.rules-modal__config-section {
  margin-top: var(--space-sm);
  border-top: 1px solid var(--color-hairline);
  padding-top: var(--space-xs);
}

.rules-modal__config-toggle {
  background: none;
  border: none;
  cursor: pointer;
  font-family: var(--font-sans);
  font-size: 13px;
  line-height: 1.43;
  letter-spacing: -0.224px;
  color: var(--color-primary);
  padding: var(--space-xxs) 0;
}

.rules-modal__config-toggle:hover {
  color: var(--color-primary-focus);
}

.rules-modal__config-body {
  margin-top: var(--space-xs);
  padding: var(--space-xs) 0;
}

.rules-modal__config-mode {
  display: flex;
  align-items: center;
  gap: var(--space-sm);
  margin-bottom: var(--space-sm);
  flex-wrap: wrap;
}

.rules-modal__config-status {
  font-family: var(--font-sans);
  font-size: 12px;
  line-height: 1.0;
  letter-spacing: -0.12px;
  color: var(--color-text-tertiary);
}

.rules-modal__config-candidates {
  margin-bottom: var(--space-xs);
}

.rules-modal__config-row {
  display: flex;
  gap: 6px;
  margin-bottom: var(--space-xxs);
  align-items: center;
}

.rules-modal__config-path {
  flex: 2;
}

.rules-modal__config-source {
  flex: 1;
}

.rules-modal__config-remove {
  background: none;
  border: none;
  cursor: pointer;
  color: var(--color-text-tertiary);
  padding: 4px;
  border-radius: var(--radius-full);
  display: flex;
  align-items: center;
  justify-content: center;
  transition: color var(--transition-fast), background-color var(--transition-fast);
}

.rules-modal__config-remove:hover {
  color: var(--color-error);
  background: var(--color-error-container);
}

.rules-modal__config-add {
  background: none;
  border: 1px dashed var(--color-hairline);
  cursor: pointer;
  font-family: var(--font-sans);
  font-size: 13px;
  line-height: 1.29;
  letter-spacing: -0.224px;
  color: var(--color-primary);
  padding: 6px var(--space-sm);
  border-radius: var(--radius-pill);
  transition: color var(--transition-fast), border-color var(--transition-fast);
}

.rules-modal__config-add:hover {
  color: var(--color-primary-focus);
  border-color: var(--color-primary-focus);
}

.rules-modal__config-actions {
  margin-top: var(--space-xs);
}

.rules-modal__btn--small {
  font-size: 13px;
  padding: 6px var(--space-sm);
}

/* --- 右键菜单：复制 / 撤回 / 删除 --- */
.ai-chat-panel__ctx-menu {
  position: fixed;
  z-index: 3000;
  min-width: 160px;
  margin: 0;
  padding: 4px 0;
  list-style: none;
  background: var(--bg-elevated, #ffffff);
  border: 1px solid var(--border-color, #e4e7ed);
  border-radius: 6px;
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.12);
  user-select: none;
}

.ai-chat-panel__ctx-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 14px;
  font-size: 13px;
  color: var(--text-primary, #303133);
  cursor: pointer;
  transition: background-color 0.15s ease;
}

.ai-chat-panel__ctx-item:hover {
  background: var(--bg-hover, #f5f7fa);
}

.ai-chat-panel__ctx-item--danger {
  color: var(--color-danger, #f56c6c);
}

.ai-chat-panel__ctx-item--danger:hover {
  background: rgba(245, 108, 108, 0.1);
}
</style>
