<script setup lang="ts">
// Koyori IDE 组件 · Ai Settings View；交互服务：窗口（WindowService）。
// 喵，这是 Ai Settings View，负责 Koyori IDE 的界面呈现喵~
import { nextTick, onBeforeUnmount, onMounted, ref } from "vue";
import AiSection from "@/components/settings/AiSection.vue";
import AgentSection from "@/components/settings/AgentSection.vue";
import PromptsSection from "@/components/settings/PromptsSection.vue";
import PresetsSection from "@/components/settings/PresetsSection.vue";
import McpSection from "@/components/settings/ai/McpSection.vue";
import SkillsSection from "@/components/settings/ai/SkillsSection.vue";
import ComputerUseSection from "@/components/settings/ai/ComputerUseSection.vue";
import ImSection from "@/components/settings/ai/ImSection.vue";
import PersonaSection from "@/components/settings/ai/PersonaSection.vue";
import ModelPermissionSection from "@/components/settings/ai/ModelPermissionSection.vue";
import DiffSection from "@/components/settings/ai/DiffSection.vue";
import RollbackSection from "@/components/settings/ai/RollbackSection.vue";
import PersonalizationSection from "@/components/settings/ai/PersonalizationSection.vue";
import AiWindowThemePicker from "./AiWindowThemePicker.vue";
import { windowService } from "@/api/services";
import { appState, saveSettings } from "@/stores/app";
import {
  aiWindowState,
  applyAIWindowTheme,
  setAISidebarWidth,
  setAITerminalWidth,
  type AIWindowTheme,
} from "@/stores/aiWindow";
import { useI18n } from "@/lib/i18n";
import { subscribeCrossWindowEvent } from "@/lib/crossWindowSync";
import {
  consumePendingAISettingsSection,
  parseAISettingsSectionEvent,
  type AISettingsSection,
} from "@/stores/aiAssistant";

type SettingsGroup = "models" | "context" | "execution" | "integrations" | "window";

const { t } = useI18n();
const activeGroup = ref<SettingsGroup>("models");
const alwaysOnTop = ref(true);

const groups: Array<{ key: SettingsGroup; labelKey: string; descriptionKey: string }> = [
  { key: "models", labelKey: "aiWorkspace.settingsModels", descriptionKey: "aiWorkspace.settingsModelsDesc" },
  { key: "context", labelKey: "aiWorkspace.settingsContext", descriptionKey: "aiWorkspace.settingsContextDesc" },
  { key: "execution", labelKey: "aiWorkspace.settingsExecution", descriptionKey: "aiWorkspace.settingsExecutionDesc" },
  { key: "integrations", labelKey: "aiWorkspace.settingsIntegrations", descriptionKey: "aiWorkspace.settingsIntegrationsDesc" },
  { key: "window", labelKey: "aiWorkspace.settingsWindow", descriptionKey: "aiWorkspace.settingsWindowDesc" },
];

const settingsGroupBySection: Record<AISettingsSection, SettingsGroup> = {
  ai: "models",
  agent: "models",
  prompts: "context",
  presets: "context",
  computerUse: "execution",
};

function openSettingsSection(section: AISettingsSection): void {
  activeGroup.value = settingsGroupBySection[section];
  void nextTick(() => {
    document.querySelector<HTMLElement>(`[data-ai-settings-section="${section}"]`)
      ?.scrollIntoView?.({ block: "start" });
  });
}

const unsubscribeOpenSettings = subscribeCrossWindowEvent("ai:open-settings", (event) => {
  const section = parseAISettingsSectionEvent(event);
  if (!section) return;
  consumePendingAISettingsSection();
  openSettingsSection(section);
});

function updateTheme(theme: AIWindowTheme): void {
  aiWindowState.theme = theme;
  appState.aiWindowTheme = theme;
  applyAIWindowTheme(theme);
  saveSettings();
}

function updateSidebarWidth(value: number): void {
  const width = setAISidebarWidth(value);
  appState.aiSidebarWidth = width;
  saveSettings();
}

function updateTerminalWidth(value: number): void {
  const width = setAITerminalWidth(value);
  appState.aiTerminalWidth = width;
  saveSettings();
}

async function toggleAlwaysOnTop(): Promise<void> {
  alwaysOnTop.value = !alwaysOnTop.value;
  await windowService.setAIAlwaysOnTop(alwaysOnTop.value);
}

onMounted(async () => {
  const pendingSection = consumePendingAISettingsSection();
  if (pendingSection) openSettingsSection(pendingSection);
  try { alwaysOnTop.value = await windowService.isAIAlwaysOnTop(); }
  catch { alwaysOnTop.value = true; }
});

onBeforeUnmount(unsubscribeOpenSettings);
</script>

<template>
  <section class="ai-settings-view">
    <header class="ai-settings-view__header">
      <p class="ai-settings-view__eyebrow">{{ t("aiWorkspace.settings") }}</p>
      <h1>{{ t("aiWorkspace.settings") }}</h1>
      <p>{{ t("aiWorkspace.settingsDesc") }}</p>
    </header>

    <div class="ai-settings-view__layout">
      <nav class="ai-settings-view__nav" :aria-label="t('aiWorkspace.settings')">
        <button
          v-for="group in groups"
          :key="group.key"
          type="button"
          class="ai-settings-view__nav-item"
          :class="{ 'is-active': activeGroup === group.key }"
          :data-group="group.key"
          @click="activeGroup = group.key"
        >
          <strong>{{ t(group.labelKey) }}</strong>
          <small>{{ t(group.descriptionKey) }}</small>
        </button>
      </nav>

      <main class="ai-settings-view__content">
        <template v-if="activeGroup === 'models'">
          <section data-ai-settings-section="ai"><AiSection /></section>
          <section data-ai-settings-section="agent"><AgentSection /></section>
          <PersonaSection /><ModelPermissionSection />
        </template>
        <template v-else-if="activeGroup === 'context'">
          <McpSection /><SkillsSection />
          <section data-ai-settings-section="prompts"><PromptsSection /></section>
          <section data-ai-settings-section="presets"><PresetsSection /></section>
        </template>
        <template v-else-if="activeGroup === 'execution'">
          <section data-ai-settings-section="computerUse"><ComputerUseSection /></section>
          <DiffSection /><RollbackSection />
        </template>
        <template v-else-if="activeGroup === 'integrations'">
          <ImSection /><PersonalizationSection />
        </template>
        <div v-else class="ai-settings-window">
          <section class="ai-settings-card">
            <h2>{{ t("aiWorkspace.windowTheme") }}</h2>
            <p>{{ t("aiWorkspace.windowThemeDesc") }}</p>
            <AiWindowThemePicker :theme="aiWindowState.theme" @update:theme="updateTheme" />
          </section>
          <section class="ai-settings-card ai-settings-card__rows">
            <label>
              <span><strong>{{ t("general.openAIWindowOnStartup") }}</strong><small>{{ t("general.openAIWindowOnStartupHint") }}</small></span>
              <input v-model="appState.openAIWindowOnStartup" type="checkbox" @change="saveSettings" />
            </label>
            <label>
              <span><strong>{{ t("aiWindow.alwaysOnTop") }}</strong><small>{{ t("aiWorkspace.alwaysOnTopDesc") }}</small></span>
              <input type="checkbox" :checked="alwaysOnTop" @change="toggleAlwaysOnTop" />
            </label>
            <label>
              <span><strong>{{ t("aiWorkspace.sidebarWidth") }}</strong><small>{{ aiWindowState.sidebarWidth }}px</small></span>
              <input :value="aiWindowState.sidebarWidth" type="range" min="260" max="380" step="8" @input="updateSidebarWidth(Number(($event.target as HTMLInputElement).value))" />
            </label>
            <label>
              <span><strong>{{ t("aiWorkspace.terminalWidth") }}</strong><small>{{ aiWindowState.terminalWidth }}px</small></span>
              <input :value="aiWindowState.terminalWidth" type="range" min="340" max="960" step="10" @input="updateTerminalWidth(Number(($event.target as HTMLInputElement).value))" />
            </label>
          </section>
        </div>
      </main>
    </div>
  </section>
</template>

<style scoped>
.ai-settings-view { height: 100%; overflow: auto; padding: 24px; }
.ai-settings-view__header { max-width: 760px; margin-bottom: 20px; }
.ai-settings-view__eyebrow { color: var(--color-primary); font-size: 12px; font-weight: 600; letter-spacing: .08em; text-transform: uppercase; }
.ai-settings-view__header h1 { margin: 4px 0 6px; font-size: 28px; }
.ai-settings-view__header > p:last-child { color: var(--color-text-secondary); }
.ai-settings-view__layout { display: grid; grid-template-columns: 210px minmax(0, 1fr); gap: 22px; align-items: start; }
.ai-settings-view__nav { position: sticky; top: 0; display: grid; gap: 4px; }
.ai-settings-view__nav-item { display: grid; gap: 2px; padding: 10px 12px; border: 0; border-radius: var(--radius-sm); color: var(--color-text-secondary); background: transparent; text-align: left; cursor: pointer; }
.ai-settings-view__nav-item:hover { background: var(--chrome-hover-bg); }
.ai-settings-view__nav-item.is-active { color: var(--color-text-primary); background: var(--chrome-active-bg); }
.ai-settings-view__nav-item small { color: var(--color-text-tertiary); font-size: 10px; }
.ai-settings-view__content { min-width: 0; max-width: 1120px; }
.ai-settings-view__content :deep(.settings-section) { padding: 18px; margin-bottom: 14px; border: 1px solid var(--color-border-default); border-radius: var(--radius-lg); background: var(--color-bg-surface); }
.ai-settings-card { padding: 18px; margin-bottom: 14px; border: 1px solid var(--color-border-default); border-radius: var(--radius-lg); background: var(--color-bg-surface); }
.ai-settings-card h2 { margin-bottom: 4px; font-size: 18px; }
.ai-settings-card > p { margin-bottom: 14px; color: var(--color-text-secondary); font-size: 13px; }
.ai-settings-card__rows { display: grid; gap: 4px; }
.ai-settings-card__rows label { display: flex; align-items: center; justify-content: space-between; gap: 20px; min-height: 54px; padding: 8px 0; border-bottom: 1px solid var(--color-border-subtle); }
.ai-settings-card__rows label:last-child { border-bottom: 0; }
.ai-settings-card__rows label > span { display: grid; gap: 2px; }
.ai-settings-card__rows small { color: var(--color-text-tertiary); font-size: 11px; }
.ai-settings-card__rows input[type="range"] { width: min(280px, 42vw); }
@media (max-width: 860px) { .ai-settings-view__layout { grid-template-columns: 1fr; } .ai-settings-view__nav { position: static; grid-template-columns: repeat(2, 1fr); } }
</style>
