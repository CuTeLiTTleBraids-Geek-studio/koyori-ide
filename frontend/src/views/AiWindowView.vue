<script setup lang="ts">
// Koyori IDE 组件 · Ai Window View；交互服务：对话历史（ConversationService）、项目（ProjectService）、窗口（WindowService）。
// 喵，这是 Ai Window View，负责 Koyori IDE 的界面呈现喵~
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { Events } from "@wailsio/runtime";
import { Document, FolderOpened, Top, Minus, FullScreen, Close } from "@element-plus/icons-vue";
import MessageList from "@/components/ai-assistant/MessageList.vue";
import InputComposer from "@/components/ai-assistant/InputComposer.vue";
import SnapshotTimeline from "@/components/ai-assistant/SnapshotTimeline.vue";
import AiWorkspaceSidebar from "@/components/ai-window/AiWorkspaceSidebar.vue";
import AiTerminalDock from "@/components/ai-window/AiTerminalDock.vue";
import AiSkillsView from "@/components/ai-window/AiSkillsView.vue";
import AiAutomationView from "@/components/ai-window/AiAutomationView.vue";
import AiSettingsView from "@/components/ai-window/AiSettingsView.vue";
import { aiState, loadConversation, clearMessages, addContextChip } from "@/stores/ai";
import {
  aiAssistantState,
  acknowledgeAIConversationTarget,
  parseAIConversationTargetEvent,
  readPendingAIConversationTarget,
  registerAIConversationTargetReceiver,
  setActiveConversation,
  switchMode,
  unregisterAIConversationTargetReceiver,
  type AIConversationTarget,
} from "@/stores/aiAssistant";
import { appState, openProject, saveSettings } from "@/stores/app";
import {
  aiWindowState,
  applyAIWindowTheme,
  getTerminalMaxWidth,
  setAISidebarWidth,
  setAITerminalWidth,
} from "@/stores/aiWindow";
import { conversationService, windowService, projectService } from "@/api/services";
import { useI18n } from "@/lib/i18n";
import { notifyError, notifySuccess, notifyWarning } from "@/lib/notifications";
import type { Project } from "@/types";
import { agentMcpTools, refreshAgentMcpTools } from "@/stores/mcp";
import { skillsList, loadSkills } from "@/stores/skills";
import { listSnapshots } from "@/stores/snapshot";
import { subscribeCrossWindowEvent } from "@/lib/crossWindowSync";

const { t } = useI18n();

type ComposerExpose = { handleAttach?: () => void };
const composer = ref<ComposerExpose | null>(null);
const editingTitle = ref(false);
const titleDraft = ref("");
const alwaysOnTop = ref(true);
// BUG6: AI 窗口最大化状态，由 ai-window:maximised 事件驱动。
const isAIMax = ref(false);
const lastSelectionPath = ref("");
const viewportWidth = ref(window.innerWidth);

const projects = ref<Project[]>([]);
const selectedWorkspace = ref(appState.currentProject || "");
const selectedMcp = ref<string[]>([]);
const selectedSkills = ref<string[]>([]);
const showWorkspaceMenu = ref(false);
const showMcpMenu = ref(false);
const showSkillsMenu = ref(false);
const showModelMenu = ref(false);
const switchingWorkspace = ref(false);

const conversationTitle = computed(() => {
  if (aiState.currentConversationTitle) return aiState.currentConversationTitle;
  if (aiState.currentConversationId) return t("aiWindow.untitledConversation");
  return t("aiWindow.newConversation");
});

const modeLabel = computed(() => {
  const mode = aiAssistantState.mode;
  if (mode === "plan") return t("aiAssistant.modePlan");
  if (mode === "goal") return t("aiAssistant.modeGoal");
  if (mode === "agent") return t("aiAssistant.modeAgent");
  return t("aiAssistant.modeChat");
});

const modelOptions = computed(() => appState.aiProviderConfigs.map((cfg) => ({
  label: `${cfg.provider || cfg.name || "AI"}: ${cfg.model || "—"}`,
  value: cfg.model || "",
  configId: cfg.id,
})));

const currentModelLabel = computed(() => appState.aiModel || t("aiAssistant.noModel"));
const terminalMaxWidth = computed(() => getTerminalMaxWidth(
  Math.max(620, viewportWidth.value - aiWindowState.sidebarWidth),
));

function closeMenus(): void {
  showWorkspaceMenu.value = false;
  showMcpMenu.value = false;
  showSkillsMenu.value = false;
  showModelMenu.value = false;
}

async function handleSelectConversation(id: string): Promise<void> {
  aiWindowState.activeView = "assistant";
  if (!id) {
    clearMessages();
    return;
  }
  await loadConversation(id);
}

function startEditTitle(): void {
  titleDraft.value = conversationTitle.value;
  editingTitle.value = true;
}

async function commitTitle(): Promise<void> {
  editingTitle.value = false;
  const next = titleDraft.value.trim();
  if (!next || !aiState.currentConversationId) return;
  try {
    await conversationService.updateTitle(aiState.currentConversationId, next);
    aiState.currentConversationTitle = next;
  } catch (error) {
    notifyError(error instanceof Error ? error.message : String(error));
  }
}

function resizeSidebar(width: number): void {
  setAISidebarWidth(width);
}

function persistSidebar(width: number): void {
  appState.aiSidebarWidth = setAISidebarWidth(width);
  saveSettings();
}

function resizeTerminal(width: number): void {
  setAITerminalWidth(Math.min(width, terminalMaxWidth.value));
}

function persistTerminal(width: number): void {
  appState.aiTerminalWidth = setAITerminalWidth(Math.min(width, terminalMaxWidth.value));
  saveSettings();
}

function toggleTerminal(): void {
  aiWindowState.terminalVisible = !aiWindowState.terminalVisible;
  if (aiWindowState.terminalVisible) appState.terminalVisible = true;
}

function closeTerminal(): void {
  aiWindowState.terminalVisible = false;
}

async function selectWorkspace(path: string): Promise<void> {
  if (!path || switchingWorkspace.value || path === appState.currentProject) {
    closeMenus();
    return;
  }
  switchingWorkspace.value = true;
  try {
    const recent = projects.value.find((item) => item.path === path);
    await openProject(recent?.name ?? path, path);
    selectedWorkspace.value = appState.currentProject ?? path;
    closeMenus();
  } catch (error) {
    notifyError(t("projects.openProjectFailed", {
      error: error instanceof Error ? error.message : String(error),
    }));
  } finally {
    switchingWorkspace.value = false;
  }
}

function toggleMcp(namespace: string): void {
  const index = selectedMcp.value.indexOf(namespace);
  if (index >= 0) selectedMcp.value.splice(index, 1);
  else selectedMcp.value.push(namespace);
}

function toggleSkill(id: string): void {
  const index = selectedSkills.value.indexOf(id);
  if (index >= 0) selectedSkills.value.splice(index, 1);
  else selectedSkills.value.push(id);
}

function selectModel(model: string, configId: string): void {
  if (model) appState.aiModel = model;
  if (configId) appState.activeAIConfigId = configId;
  saveSettings();
  closeMenus();
}

async function openInExplorer(): Promise<void> {
  const path = selectedWorkspace.value || appState.currentProject;
  if (!path) return notifyWarning(t("aiWindow.noWorkspace"));
  try { await windowService.openPathInExplorer(path); }
  catch (error) { notifyError(error instanceof Error ? error.message : String(error)); }
}

async function openInVSCode(): Promise<void> {
  const path = selectedWorkspace.value || appState.currentProject;
  if (!path) return notifyWarning(t("aiWindow.noWorkspace"));
  try { await windowService.openPathInVSCode(path); }
  catch (error) { notifyError(error instanceof Error ? error.message : String(error)); }
}

async function toggleAlwaysOnTop(): Promise<void> {
  alwaysOnTop.value = !alwaysOnTop.value;
  try { await windowService.setAIAlwaysOnTop(alwaysOnTop.value); }
  catch (error) { notifyError(error instanceof Error ? error.message : String(error)); }
}

// BUG6: AI 窗口 Frameless 模式下的窗口控制。
function handleAIMinimise(): void {
  windowService.minimiseAIWindow();
}
function handleAIMaximiseToggle(): void {
  windowService.toggleMaximiseAIWindow();
}
function handleAIClose(): void {
  windowService.closeAIWindow();
}

function onSelectionEvent(data: unknown): void {
  const payload = (Array.isArray(data) ? data[0] : data) as
    | { code?: string; language?: string; filePath?: string }
    | undefined;
  if (!payload?.code) return;
  const path = payload.filePath || "selection";
  if (payload.filePath && payload.filePath !== "untitled") lastSelectionPath.value = payload.filePath;
  addContextChip({
    id: `sel-${Date.now()}`,
    kind: "codeblock",
    label: path.split(/[/\\]/).pop() || path,
    content: payload.code,
    language: payload.language || "text",
  });
  aiWindowState.activeView = "assistant";
  notifySuccess(t("aiWindow.selectionReceived"));
}

function handleMessageClick(event: MouseEvent): void {
  const target = event.target as HTMLElement | null;
  if (!target?.classList.contains("code-block-apply-btn")) return;
  const code = target.closest(".code-block-wrap")?.querySelector("pre")?.textContent ?? "";
  const filePath = lastSelectionPath.value || appState.currentFilePath || "";
  if (!filePath) return notifyError(t("aiWindow.noActiveFile"), t("aiWindow.applyTitle"));
  void Events.Emit("ai:apply-to-editor", { code, filePath, language: "" });
  notifySuccess(t("aiWindow.applySent"));
}

let unsubSelection: (() => void) | null = null;
let unsubAIMaximised: (() => void) | null = null;
let unsubConversationTarget: (() => void) | null = null;
let systemThemeQuery: MediaQueryList | null = null;
let conversationTargetPoll: ReturnType<typeof setInterval> | null = null;
let conversationTargetReceiverEpoch = "";
let conversationTargetApplyGeneration = 0;
let applyingConversationTarget: AIConversationTarget | null = null;
const appliedTargetBySource = new Map<string, AIConversationTarget>();
let latestAppliedTarget: AIConversationTarget | null = null;
const retiredTargetEpochs = new Set<string>();
const appliedTargetRequests = new Set<string>();
let deferredConversationTarget: AIConversationTarget | null = null;

function isNewerConversationTarget(
  target: AIConversationTarget,
  reference: AIConversationTarget | null | undefined,
): boolean {
  if (!reference) return target.sequence <= 1024;
  if (target.sourceOrigin !== reference.sourceOrigin) return target.sequence <= 1024;
  if (target.sourceEpoch === reference.sourceEpoch) {
    return target.sequence > reference.sequence && target.sequence - reference.sequence <= 1024;
  }
  return !retiredTargetEpochs.has(target.sourceEpoch) && target.createdAt >= reference.createdAt;
}

function isOlderAcrossSources(
  target: AIConversationTarget,
  reference: AIConversationTarget | null | undefined,
): boolean {
  return Boolean(
    reference &&
    target.sourceOrigin !== reference.sourceOrigin &&
    target.createdAt < reference.createdAt,
  );
}

function canApplyConversationTarget(target: AIConversationTarget): boolean {
  if (appliedTargetRequests.has(target.requestId) || retiredTargetEpochs.has(target.sourceEpoch)) {
    return false;
  }
  const applied = appliedTargetBySource.get(target.sourceOrigin);
  if (!isNewerConversationTarget(target, applied)) return false;
  if (isOlderAcrossSources(target, latestAppliedTarget)) return false;
  if (isOlderAcrossSources(target, applyingConversationTarget)) return false;
  if (isOlderAcrossSources(target, deferredConversationTarget)) return false;
  if (
    applyingConversationTarget?.sourceOrigin === target.sourceOrigin &&
    !isNewerConversationTarget(target, applyingConversationTarget)
  ) return false;
  if (
    deferredConversationTarget?.sourceOrigin === target.sourceOrigin &&
    !isNewerConversationTarget(target, deferredConversationTarget)
  ) return false;
  return true;
}

function commitConversationTarget(target: AIConversationTarget): void {
  const previous = appliedTargetBySource.get(target.sourceOrigin);
  if (previous && previous.sourceEpoch !== target.sourceEpoch) {
    retiredTargetEpochs.add(previous.sourceEpoch);
  }
  appliedTargetBySource.set(target.sourceOrigin, target);
  latestAppliedTarget = target;
  appliedTargetRequests.add(target.requestId);
  if (appliedTargetRequests.size > 256) {
    const oldest = appliedTargetRequests.values().next().value;
    if (typeof oldest === "string") appliedTargetRequests.delete(oldest);
  }
}

async function applyConversationTarget(target: AIConversationTarget): Promise<void> {
  if (!canApplyConversationTarget(target)) return;
  if (aiState.streaming || aiState.globalStreamBusy) {
    deferredConversationTarget = target;
    return;
  }
  deferredConversationTarget = null;
  applyingConversationTarget = target;
  const applyGeneration = ++conversationTargetApplyGeneration;
  let committed = false;
  try {
    aiWindowState.activeView = "assistant";
    if (!target.conversationId) {
      if (!clearMessages()) return;
      setActiveConversation(null);
    } else {
      const loaded = await loadConversation(target.conversationId);
      // loadConversation is fail-closed: an error leaves the current messages
      // untouched and returns false. Never commit/ACK a target that was not
      // actually loaded, otherwise the durable retry is lost and the window
      // appears to have refreshed while still showing stale content.
      if (!loaded) return;
      if (
        applyGeneration !== conversationTargetApplyGeneration ||
        aiState.currentConversationId !== target.conversationId
      ) return;
      setActiveConversation(target.conversationId);
    }
    if (applyGeneration !== conversationTargetApplyGeneration) return;
    switchMode(target.mode);
    commitConversationTarget(target);
    acknowledgeAIConversationTarget(target, conversationTargetReceiverEpoch);
    committed = true;
  } finally {
    if (
      applyGeneration === conversationTargetApplyGeneration &&
      applyingConversationTarget?.requestId === target.requestId
    ) {
      applyingConversationTarget = null;
    }
    if (!committed && applyGeneration === conversationTargetApplyGeneration) {
      // Keep the durable target intact. A later live retry or remount may
      // apply it after the transient backend load failure has cleared.
      deferredConversationTarget = null;
    }
  }
}

function onConversationTargetEvent(event: unknown): void {
  const target = parseAIConversationTargetEvent(event, conversationTargetReceiverEpoch);
  if (!target) return;
  if (appliedTargetRequests.has(target.requestId)) {
    // ACK delivery can fail independently from target delivery. Re-ACK exact
    // retries without reloading or reapplying the already committed target.
    acknowledgeAIConversationTarget(target, conversationTargetReceiverEpoch);
    return;
  }
  void applyConversationTarget(target);
}

function pollPendingConversationTarget(): void {
  const target = readPendingAIConversationTarget(conversationTargetReceiverEpoch);
  if (!target) return;
  if (appliedTargetRequests.has(target.requestId)) {
    acknowledgeAIConversationTarget(target, conversationTargetReceiverEpoch);
    return;
  }
  void applyConversationTarget(target);
}

function applyCurrentAITheme(): void {
  applyAIWindowTheme(aiWindowState.theme);
}

function handleViewportResize(): void {
  viewportWidth.value = window.innerWidth;
  if (aiWindowState.terminalWidth > terminalMaxWidth.value) {
    setAITerminalWidth(terminalMaxWidth.value);
  }
}

onMounted(async () => {
  conversationTargetReceiverEpoch = registerAIConversationTargetReceiver();
  try {
    unsubConversationTarget = subscribeCrossWindowEvent(
      "ai:open-conversation",
      onConversationTargetEvent,
    );
  } catch {
    unsubConversationTarget = null;
  }
  const pendingConversationTarget = readPendingAIConversationTarget(
    conversationTargetReceiverEpoch,
  );
  if (pendingConversationTarget) void applyConversationTarget(pendingConversationTarget);
  conversationTargetPoll = globalThis.setInterval(pollPendingConversationTarget, 250);

  aiWindowState.theme = appState.aiWindowTheme;
  setAISidebarWidth(appState.aiSidebarWidth);
  setAITerminalWidth(appState.aiTerminalWidth);
  applyCurrentAITheme();
  window.addEventListener("resize", handleViewportResize);

  systemThemeQuery = typeof window.matchMedia === "function"
    ? window.matchMedia("(prefers-color-scheme: light)")
    : null;
  systemThemeQuery?.addEventListener?.("change", applyCurrentAITheme);

  try { alwaysOnTop.value = await windowService.isAIAlwaysOnTop(); }
  catch { alwaysOnTop.value = true; }
  // BUG6: 初始化 AI 窗口最大化状态。
  try { isAIMax.value = await windowService.isAIWindowMaximised(); }
  catch { isAIMax.value = false; }
  try { projects.value = await projectService.getRecentProjects(); }
  catch { projects.value = []; }

  void refreshAgentMcpTools();
  void loadSkills();
  void listSnapshots();

  try {
    unsubSelection = subscribeCrossWindowEvent("ai:selection", (event: unknown) => {
      const data = event && typeof event === "object" && "data" in event
        ? (event as { data: unknown }).data
        : event;
      onSelectionEvent(data);
    });
  } catch {
    unsubSelection = null;
  }
  // BUG6: 监听 AI 窗口最大化/还原事件，更新图标状态。
  try {
    unsubAIMaximised = subscribeCrossWindowEvent("ai-window:maximised", (event: unknown) => {
      const data = event && typeof event === "object" && "data" in event
        ? (event as { data: unknown }).data
        : event;
      isAIMax.value = data === true;
    });
  } catch {
    unsubAIMaximised = null;
  }
});

onBeforeUnmount(() => {
  window.removeEventListener("resize", handleViewportResize);
  systemThemeQuery?.removeEventListener?.("change", applyCurrentAITheme);
  unsubSelection?.();
  unsubAIMaximised?.();
  unsubConversationTarget?.();
  if (conversationTargetPoll !== null) {
    globalThis.clearInterval(conversationTargetPoll);
    conversationTargetPoll = null;
  }
  unregisterAIConversationTargetReceiver(conversationTargetReceiverEpoch);
});

watch(() => aiWindowState.theme, applyCurrentAITheme);
watch(() => appState.currentProject, (path) => {
  selectedWorkspace.value = path ?? "";
});
watch(() => [aiState.streaming, aiState.globalStreamBusy] as const, ([streaming, globalBusy]) => {
  if (streaming || globalBusy || !deferredConversationTarget) return;
  const target = deferredConversationTarget;
  deferredConversationTarget = null;
  void applyConversationTarget(target);
});
watch(
  () => [appState.theme, appState.designLanguage],
  () => applyCurrentAITheme(),
);
</script>

<template>
  <div class="ai-window" @pointerdown.self="closeMenus" @keydown.esc="closeMenus">
    <AiWorkspaceSidebar
      :active-view="aiWindowState.activeView"
      :width="aiWindowState.sidebarWidth"
      :terminal-visible="aiWindowState.terminalVisible"
      @select-view="aiWindowState.activeView = $event"
      @select-conversation="handleSelectConversation"
      @toggle-terminal="toggleTerminal"
      @resize="resizeSidebar"
      @resize-commit="persistSidebar"
    />

    <main class="ai-window__main">
      <header class="ai-window__top">
        <div class="ai-window__heading">
          <div
            v-if="!editingTitle"
            class="ai-window__title"
            role="button"
            tabindex="0"
            :aria-label="t('aiWindow.editTitleHint')"
            @dblclick="startEditTitle"
            @keydown.enter.prevent="startEditTitle"
          >
            {{ aiWindowState.activeView === "assistant" ? conversationTitle : t(`aiWorkspace.${aiWindowState.activeView}`) }}
          </div>
          <input
            v-else
            v-model="titleDraft"
            class="ai-window__title-input"
            autofocus
            @keydown.enter="commitTitle"
            @keydown.esc="editingTitle = false"
            @blur="commitTitle"
          />
          <span v-if="aiWindowState.activeView === 'assistant'" class="ai-window__mode-badge">{{ modeLabel }}</span>
        </div>
        <div class="ai-window__top-actions">
          <button type="button" :aria-label="t('aiWindow.actExplorer')" :title="t('aiWindow.actExplorer')" @click="openInExplorer"><el-icon><FolderOpened /></el-icon></button>
          <button type="button" :aria-label="t('aiWindow.actVSCode')" :title="t('aiWindow.actVSCode')" @click="openInVSCode"><el-icon><Document /></el-icon></button>
          <button type="button" :class="{ 'is-active': alwaysOnTop }" :aria-label="t('aiWindow.alwaysOnTop')" :title="t('aiWindow.alwaysOnTop')" @click="toggleAlwaysOnTop"><el-icon><Top /></el-icon></button>
        </div>
        <!-- BUG6: Frameless 模式下的窗口控制按钮（最小化/最大化/关闭） -->
        <div class="ai-window__window-controls">
          <button
            type="button"
            class="ai-window__window-control"
            :aria-label="t('title.minimize')"
            :title="t('title.minimize')"
            @click="handleAIMinimise"
          >
            <el-icon :size="12"><Minus /></el-icon>
          </button>
          <button
            type="button"
            class="ai-window__window-control"
            :aria-label="isAIMax ? t('title.restore') : t('title.maximize')"
            :title="isAIMax ? t('title.restore') : t('title.maximize')"
            @click="handleAIMaximiseToggle"
          >
            <el-icon v-if="!isAIMax" :size="12"><FullScreen /></el-icon>
            <svg v-else class="ai-window__restore-icon" width="12" height="12" viewBox="0 0 12 12" fill="none" xmlns="http://www.w3.org/2000/svg" aria-hidden="true">
              <rect x="1.5" y="3.5" width="7" height="7" rx="0.5" stroke="currentColor" stroke-width="1" />
              <path d="M3.5 3.5V2.5C3.5 2.22386 3.72386 2 4 2H10C10.2761 2 10.5 2.22386 10.5 2.5V8.5C10.5 8.77614 10.2761 9 10 9H9" stroke="currentColor" stroke-width="1" fill="none" />
            </svg>
          </button>
          <button
            type="button"
            class="ai-window__window-control ai-window__window-control--close"
            :aria-label="t('title.close')"
            :title="t('title.close')"
            @click="handleAIClose"
          >
            <el-icon :size="12"><Close /></el-icon>
          </button>
        </div>
      </header>

      <section class="ai-window__workspace-row">
        <div class="ai-window__workspace-main">
          <transition name="ai-view-switch" mode="out-in">
            <section v-if="aiWindowState.activeView === 'assistant'" key="assistant" class="ai-window__assistant">
              <section class="ai-window__messages" @click="handleMessageClick"><MessageList /></section>
              <footer class="ai-window__footer">
                <div class="ai-window__toolbar">
                  <button type="button" class="ai-window__tool" :aria-label="t('aiAssistant.attach')" :title="t('aiAssistant.attach')" @click="composer?.handleAttach?.()">📎</button>
                  <div class="ai-window__dropdown">
                    <button type="button" class="ai-window__tool" :aria-label="t('aiWindow.workspaceMenuAria')" aria-haspopup="menu" :aria-expanded="showWorkspaceMenu" :disabled="switchingWorkspace" @click.stop="showWorkspaceMenu = !showWorkspaceMenu; showMcpMenu = false; showSkillsMenu = false; showModelMenu = false">
                      {{ t("aiWindow.workspace") }} <span v-if="selectedWorkspace" class="ai-window__badge">1</span> ▾
                    </button>
                    <ul v-if="showWorkspaceMenu" class="ai-window__menu" role="menu" :aria-label="t('aiWindow.workspaceMenuAria')">
                      <li v-for="project in projects" :key="project.path" :class="{ 'is-selected': project.path === selectedWorkspace, 'is-disabled': switchingWorkspace }" role="menuitemradio" tabindex="0" :aria-checked="project.path === selectedWorkspace" :aria-disabled="switchingWorkspace" @click="selectWorkspace(project.path)" @keydown.enter.prevent="selectWorkspace(project.path)" @keydown.space.prevent="selectWorkspace(project.path)">{{ project.name }}</li>
                      <li v-if="!projects.length" class="ai-window__menu-empty">{{ t("aiWindow.noProjects") }}</li>
                    </ul>
                  </div>
                  <div class="ai-window__dropdown">
                    <button type="button" class="ai-window__tool" :aria-label="t('aiWindow.mcpMenuAria')" aria-haspopup="menu" :aria-expanded="showMcpMenu" @click.stop="showMcpMenu = !showMcpMenu; showWorkspaceMenu = false; showSkillsMenu = false; showModelMenu = false">MCP <span v-if="selectedMcp.length" class="ai-window__badge">{{ selectedMcp.length }}</span> ▾</button>
                    <ul v-if="showMcpMenu" class="ai-window__menu" role="menu" :aria-label="t('aiWindow.mcpMenuAria')">
                      <li v-for="tool in agentMcpTools" :key="tool.namespace" role="menuitemcheckbox" tabindex="0" :aria-checked="selectedMcp.includes(tool.namespace)" @click.stop="toggleMcp(tool.namespace)" @keydown.enter.prevent="toggleMcp(tool.namespace)" @keydown.space.prevent="toggleMcp(tool.namespace)"><input type="checkbox" tabindex="-1" aria-hidden="true" :checked="selectedMcp.includes(tool.namespace)" readonly />{{ tool.namespace }}</li>
                      <li v-if="!agentMcpTools.length" class="ai-window__menu-empty">{{ t("aiAssistant.noMcpTools") }}</li>
                    </ul>
                  </div>
                  <div class="ai-window__dropdown">
                    <button type="button" class="ai-window__tool" :aria-label="t('aiWindow.skillsMenuAria')" aria-haspopup="menu" :aria-expanded="showSkillsMenu" @click.stop="showSkillsMenu = !showSkillsMenu; showWorkspaceMenu = false; showMcpMenu = false; showModelMenu = false">Skills <span v-if="selectedSkills.length" class="ai-window__badge">{{ selectedSkills.length }}</span> ▾</button>
                    <ul v-if="showSkillsMenu" class="ai-window__menu" role="menu" :aria-label="t('aiWindow.skillsMenuAria')">
                      <li v-for="skill in skillsList" :key="skill.id" role="menuitemcheckbox" tabindex="0" :aria-checked="selectedSkills.includes(skill.id)" @click.stop="toggleSkill(skill.id)" @keydown.enter.prevent="toggleSkill(skill.id)" @keydown.space.prevent="toggleSkill(skill.id)"><input type="checkbox" tabindex="-1" aria-hidden="true" :checked="selectedSkills.includes(skill.id)" readonly />{{ skill.name }}</li>
                      <li v-if="!skillsList.length" class="ai-window__menu-empty">{{ t("aiAssistant.noSkillsAvailable") }}</li>
                    </ul>
                  </div>
                  <div class="ai-window__dropdown ai-window__dropdown--model">
                    <button type="button" class="ai-window__tool" :aria-label="t('aiWindow.modelMenuAria')" aria-haspopup="menu" :aria-expanded="showModelMenu" @click.stop="showModelMenu = !showModelMenu; showWorkspaceMenu = false; showMcpMenu = false; showSkillsMenu = false">{{ currentModelLabel }} ▾</button>
                    <ul v-if="showModelMenu" class="ai-window__menu" role="menu" :aria-label="t('aiWindow.modelMenuAria')">
                      <li v-for="model in modelOptions" :key="model.configId + model.value" :class="{ 'is-selected': model.value === appState.aiModel }" role="menuitemradio" tabindex="0" :aria-checked="model.value === appState.aiModel" @click="selectModel(model.value, model.configId)" @keydown.enter.prevent="selectModel(model.value, model.configId)" @keydown.space.prevent="selectModel(model.value, model.configId)">{{ model.label }}</li>
                      <li v-if="!modelOptions.length" class="ai-window__menu-empty">{{ t("aiAssistant.noModel") }}</li>
                    </ul>
                  </div>
                </div>
                <InputComposer ref="composer" />
              </footer>
            </section>

            <AiSkillsView v-else-if="aiWindowState.activeView === 'skills'" key="skills" />
            <AiAutomationView v-else-if="aiWindowState.activeView === 'automation'" key="automation" />
            <AiSettingsView v-else-if="aiWindowState.activeView === 'settings'" key="settings" />
            <section v-else-if="aiWindowState.activeView === 'rollback'" key="rollback" class="ai-window__feature-page"><SnapshotTimeline /></section>
          </transition>
        </div>

        <AiTerminalDock
          :visible="aiWindowState.terminalVisible"
          :width="aiWindowState.terminalWidth"
          :max-width="terminalMaxWidth"
          @close="closeTerminal"
          @resize="resizeTerminal"
          @resize-commit="persistTerminal"
        />
      </section>
    </main>
  </div>
</template>

<style scoped>
.ai-window { display: flex; width: 100vw; height: 100vh; overflow: hidden; color: var(--color-text-primary); background: var(--color-bg-base); font-family: var(--font-sans); }
.ai-window__main { display: flex; flex: 1; min-width: 0; flex-direction: column; overflow: hidden; }
.ai-window__top { display: flex; align-items: center; justify-content: space-between; gap: 16px; min-height: 52px; padding: 0 14px 0 18px; border-bottom: 1px solid var(--color-border-default); background: var(--color-bg-surface); user-select: none; --wails-draggable: drag; }
.ai-window__heading { display: flex; min-width: 0; align-items: center; gap: 9px; --wails-draggable: none; }
.ai-window__title { overflow: hidden; color: var(--color-text-primary); font-size: 14px; font-weight: 600; text-overflow: ellipsis; white-space: nowrap; cursor: text; }
.ai-window__title-input { min-width: 240px; padding: 5px 8px; border: 1px solid var(--color-primary); border-radius: var(--radius-sm); color: var(--color-text-primary); background: var(--color-bg-elevated); outline: none; }
.ai-window__mode-badge { padding: 3px 8px; border-radius: var(--radius-pill); color: var(--color-text-secondary); background: var(--color-bg-surface-container); font-size: 10px; }
.ai-window__top-actions { display: flex; gap: 4px; --wails-draggable: none; }
.ai-window__top-actions button { display: grid; width: 32px; height: 32px; place-items: center; border: 0; border-radius: var(--radius-sm); color: var(--color-text-secondary); background: transparent; cursor: pointer; }
.ai-window__top-actions button:hover, .ai-window__top-actions button.is-active { color: var(--color-primary); background: var(--chrome-hover-bg); }
/* BUG6: Frameless 窗口控制按钮 */
.ai-window__window-controls { display: flex; align-items: center; gap: 4px; --wails-draggable: none; }
.ai-window__window-control { display: flex; align-items: center; justify-content: center; width: 28px; height: 28px; padding: 6px; border: none; border-radius: var(--radius-sm); background: transparent; color: var(--color-text-secondary); cursor: pointer; transition: color var(--transition-fast), background-color var(--transition-fast); }
.ai-window__window-control:hover { background-color: var(--chrome-hover-bg); color: var(--color-text-primary); }
.ai-window__window-control--close:hover { background-color: #d93025; color: #fff; }
.ai-window__restore-icon { display: block; flex-shrink: 0; }
.ai-window__workspace-row { display: flex; flex: 1; min-height: 0; overflow: hidden; }
.ai-window__workspace-main { position: relative; flex: 1; min-width: 0; overflow: hidden; }
.ai-window__assistant { display: flex; height: 100%; flex-direction: column; overflow: hidden; }
.ai-window__messages { display: flex; flex: 1; min-height: 0; flex-direction: column; overflow: hidden; background-image: var(--personalization-chat-bg, none); background-position: center; background-size: cover; }
.ai-window__footer { flex: 0 0 auto; border-top: 1px solid var(--color-border-default); background: var(--color-bg-surface); }
.ai-window__toolbar { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; padding: 8px 12px 0; }
.ai-window__tool { display: inline-flex; max-width: 180px; align-items: center; gap: 4px; overflow: hidden; padding: 5px 9px; border: 1px solid var(--color-border-default); border-radius: var(--radius-pill); color: var(--color-text-secondary); background: var(--color-bg-surface-container-low); font-size: 11px; text-overflow: ellipsis; white-space: nowrap; cursor: pointer; }
.ai-window__tool:hover { color: var(--color-text-primary); border-color: var(--color-primary); }
.ai-window__badge { display: inline-grid; min-width: 16px; height: 16px; padding: 0 4px; place-items: center; border-radius: var(--radius-pill); color: var(--color-on-primary); background: var(--color-primary); font-size: 9px; }
.ai-window__dropdown { position: relative; }
.ai-window__menu { position: absolute; z-index: 50; bottom: calc(100% + 5px); left: 0; min-width: 190px; max-height: 240px; overflow: auto; padding: 4px; border: 1px solid var(--color-border-default); border-radius: var(--radius-sm); background: var(--color-bg-elevated); box-shadow: var(--shadow-floating); list-style: none; }
.ai-window__menu li { display: flex; align-items: center; gap: 6px; padding: 7px 9px; border-radius: 6px; font-size: 11px; cursor: pointer; }
.ai-window__menu li:hover, .ai-window__menu li.is-selected { background: var(--chrome-active-bg); }
.ai-window__title:focus-visible, .ai-window__top-actions button:focus-visible, .ai-window__tool:focus-visible, .ai-window__menu li:focus-visible, .ai-window__window-control:focus-visible { outline: 2px solid var(--color-primary-focus); outline-offset: -2px; }
.ai-window__menu-empty { color: var(--color-text-tertiary); cursor: default !important; }
.ai-window__feature-page { height: 100%; overflow: auto; padding: 24px; }
/* 建议二: AI window sub-view switching transition — fade + slight slide.
 * Uses mode="out-in" so the leaving view fades out before the entering
 * view animates in, avoiding layout overlap. The prefers-reduced-motion
 * guard at the bottom of this block neutralizes the animation. */
.ai-view-switch-enter-active,
.ai-view-switch-leave-active { transition: opacity var(--transition-fast), transform var(--transition-fast); }
.ai-view-switch-enter-from { opacity: 0; transform: translateX(12px); }
.ai-view-switch-leave-to { opacity: 0; transform: translateX(-12px); }
.ai-window__messages :deep(.ai-msg) { max-width: 92%; padding: 16px; border-radius: var(--radius-lg); }
.ai-window__messages :deep(.ai-msg--user) { align-self: flex-end; color: var(--color-on-primary); background: var(--color-primary); }
.ai-window__messages :deep(.ai-msg--assistant) { align-self: flex-start; border: 1px solid var(--color-border-subtle); background: var(--color-bg-elevated); }
@media (prefers-reduced-motion: reduce) { .ai-window *, .ai-window *::before, .ai-window *::after { scroll-behavior: auto !important; animation-duration: .01ms !important; transition-duration: .01ms !important; } }
</style>
