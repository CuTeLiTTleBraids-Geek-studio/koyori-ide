<script setup lang="ts">
// Koyori IDE 组件 · Settings View。
// 喵，这是 Settings View，负责 Koyori IDE 的界面呈现喵~
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { Search } from "@element-plus/icons-vue";
import GeneralSection from "@/components/settings/GeneralSection.vue";
import EditorSection from "@/components/settings/EditorSection.vue";
import LspSection from "@/components/settings/LspSection.vue";
import GitSection from "@/components/settings/GitSection.vue";
import DebugSection from "@/components/settings/DebugSection.vue";
import TerminalSection from "@/components/settings/TerminalSection.vue";
import ShortcutsSection from "@/components/settings/ShortcutsSection.vue";
import AppearanceSection from "@/components/settings/AppearanceSection.vue";
import ProfileSection from "@/components/settings/ProfileSection.vue";
import LanguagePacksSection from "@/components/settings/LanguagePacksSection.vue";
// GOAL-P1-07: AI-specific settings (AI, Agent, Prompts, Presets, ComputerUse)
// have been removed from this view. They live in the AI window's AiSettingsView
// as the single writable source of truth. This file only imports the redirect
// helper so it can open that window when the user lands on an old deep-link.
import { openAIDesktopWindow, type AISettingsSection } from "@/stores/aiAssistant";
import { useI18n } from "@/lib/i18n";
import { useRoute, useRouter } from "vue-router";

// GOAL-P1-07: AI-specific sections (ai, agent, prompts, presets, computerUse)
// have been removed. Deep-links to those sections are redirected to the AI
// window. The type union is kept for backwards-compatible query parsing — an
// unknown section falls back to "general", so old bookmarks keep working.
type SettingsSection =
  | "general"
  | "editor"
  | "lsp"
  | "git"
  | "debug"
  | "terminal"
  | "shortcuts"
  | "appearance"
  | "profiles"
  | "languagePacks"
  // Legacy AI section names — deep-links land here and are immediately
  // redirected to the AI window. Not shown in the navigation.
  | "ai"
  | "agent"
  | "prompts"
  | "presets"
  | "computerUse";

const { t } = useI18n();
const route = useRoute();
const router = useRouter();
const settingsSections = new Set<SettingsSection>([
  "general", "editor", "lsp", "git", "debug", "terminal", "shortcuts",
  "appearance", "profiles",
  "languagePacks",
  // Legacy AI sections — kept so deep-links are parsed before redirecting.
  "ai", "agent", "prompts", "presets", "computerUse",
]);

// GOAL-P1-07: AI section deep-links redirect to the AI window rather than
// rendering a second writable instance of AI settings inside the main IDE.
const AI_SECTIONS = new Set<SettingsSection>(["ai", "agent", "prompts", "presets", "computerUse"]);

function sectionFromQuery(value: unknown): SettingsSection {
  return typeof value === "string" && settingsSections.has(value as SettingsSection)
    ? value as SettingsSection
    : "general";
}

const activeSection = ref<SettingsSection>(sectionFromQuery(route.query.section));
const searchQuery = ref("");
const debouncedSearchQuery = ref("");
const aiWindowOpenFailed = ref(false);
const pendingAISection = ref<AISettingsSection | null>(null);
let searchTimer: ReturnType<typeof setTimeout> | null = null;

const primaryNavItems: { key: SettingsSection; labelKey: string }[] = [
  { key: "general", labelKey: "settings.general" },
  { key: "editor", labelKey: "settings.editor" },
  { key: "lsp", labelKey: "settings.lsp" },
  { key: "git", labelKey: "activity.sourceControl" },
  { key: "debug", labelKey: "view.debug.title" },
  { key: "terminal", labelKey: "settings.terminal" },
  { key: "shortcuts", labelKey: "settings.shortcuts" },
  { key: "appearance", labelKey: "settings.appearance" },
  { key: "profiles", labelKey: "settings.profiles" },
  { key: "languagePacks", labelKey: "settings.languagePacks" },
  // GOAL-P1-07: AI settings removed from nav — they live in the AI window.
  // The "open AI settings" entry below replaces them with a single redirect.
];

const experimentalNavItems: { key: SettingsSection; labelKey: string }[] = [
  // GOAL-P1-07: computerUse moved to AI window; removed from experimental nav.
];

// GOAL-P1-07: redirect AI section deep-links to the AI window.
// Called on initial render and whenever the route query changes.
async function openAISettings(section: AISettingsSection): Promise<void> {
  pendingAISection.value = section;
  aiWindowOpenFailed.value = false;
  activeSection.value = "general";
  try {
    await openAIDesktopWindow(section);
    pendingAISection.value = null;
    await router.replace({ query: { section: "general" } });
  } catch {
    aiWindowOpenFailed.value = true;
  }
}

function redirectAISectionIfNeeded(section: SettingsSection): void {
  if (AI_SECTIONS.has(section)) {
    void openAISettings(section as AISettingsSection);
  }
}

function retryAISettings(): void {
  if (pendingAISection.value) void openAISettings(pendingAISection.value);
}
const allNavItems = [...primaryNavItems, ...experimentalNavItems];

interface SettingsSearchEntry {
  section: SettingsSection;
  labelKey: string;
}

const searchEntries: SettingsSearchEntry[] = [
  { section: "general", labelKey: "settings.general" },
  { section: "editor", labelKey: "editorSection.fontSize" },
  { section: "editor", labelKey: "editorSection.fontFamily" },
  { section: "editor", labelKey: "editorSection.tabSize" },
  { section: "editor", labelKey: "editorSection.wordWrap" },
  { section: "editor", labelKey: "editorSection.lineNumbers" },
  { section: "editor", labelKey: "editorSection.minimap" },
  { section: "editor", labelKey: "editorSection.stickyScroll" },
  { section: "editor", labelKey: "editorSection.bracketColorization" },
  { section: "editor", labelKey: "editorSection.autoSave" },
  { section: "lsp", labelKey: "settings.lsp" },
  { section: "lsp", labelKey: "editorSection.organizeImportsOnSave" },
  { section: "lsp", labelKey: "editorSection.inlayHints" },
  { section: "git", labelKey: "editorSection.gitBlame" },
  { section: "debug", labelKey: "view.debug.title" },
  { section: "terminal", labelKey: "settings.terminal" },
  { section: "appearance", labelKey: "settings.appearance" },
  { section: "shortcuts", labelKey: "shortcuts.title" },
  { section: "profiles", labelKey: "settings.profiles" },
  { section: "languagePacks", labelKey: "settings.languagePacks" },
  { section: "ai", labelKey: "settings.ai" },
  { section: "agent", labelKey: "settings.agent" },
  { section: "prompts", labelKey: "settings.prompts" },
  { section: "presets", labelKey: "settings.presets" },
  { section: "computerUse", labelKey: "computerUseSection.experimentalLabel" },
];

const searchResults = computed(() => {
  const query = debouncedSearchQuery.value.trim().toLocaleLowerCase();
  if (!query) return [];
  return searchEntries.filter((entry) => {
    const section = allNavItems.find((item) => item.key === entry.section);
    return `${t(entry.labelKey)} ${section ? t(section.labelKey) : ""}`
      .toLocaleLowerCase()
      .includes(query);
  });
});

const searching = computed(() => debouncedSearchQuery.value.trim().length > 0);
const visibleNavItems = computed(() => {
  if (!searching.value) return allNavItems;
  const matches = new Set(searchResults.value.map((entry) => entry.section));
  return allNavItems.filter((item) => matches.has(item.key));
});

const visiblePrimaryNavItems = computed(() =>
  visibleNavItems.value.filter((item) => primaryNavItems.includes(item)),
);
const visibleExperimentalNavItems = computed(() =>
  visibleNavItems.value.filter((item) => experimentalNavItems.includes(item)),
);

watch(searchQuery, (value) => {
  if (searchTimer) {
    clearTimeout(searchTimer);
    searchTimer = null;
  }
  if (!value.trim()) {
    debouncedSearchQuery.value = "";
    return;
  }
  searchTimer = setTimeout(() => {
    searchTimer = null;
    debouncedSearchQuery.value = value;
  }, 500);
});

async function selectSection(key: SettingsSection) {
  // GOAL-P1-07: block navigation into AI sections; redirect to AI window.
  if (AI_SECTIONS.has(key)) {
    redirectAISectionIfNeeded(key);
    return;
  }
  activeSection.value = key;
  await router.replace({ query: { ...route.query, section: key } });
}

// Redirect on initial load when an AI section deep-link was followed.
redirectAISectionIfNeeded(activeSection.value);

function openSearchResult(entry: SettingsSearchEntry) {
  void selectSection(entry.section);
  searchQuery.value = "";
  debouncedSearchQuery.value = "";
}

watch(() => route.query.section, (section) => {
  activeSection.value = sectionFromQuery(section);
  redirectAISectionIfNeeded(activeSection.value);
});

onBeforeUnmount(() => {
  if (searchTimer) clearTimeout(searchTimer);
});
</script>

<template>
  <div class="settings-view">
    <div v-if="aiWindowOpenFailed" class="settings-ai-window-error" role="alert">
      <span>{{ t("settings.aiWindowOpenFailed") }}</span>
      <button type="button" data-test="retry-ai-settings" @click="retryAISettings">
        {{ t("common.retry") }}
      </button>
    </div>
    <aside class="settings-nav">
      <div class="settings-search">
        <el-icon class="settings-search__icon" aria-hidden="true"><Search /></el-icon>
        <input
          v-model="searchQuery"
          type="search"
          class="settings-search__input"
          :placeholder="t('settings.searchPlaceholder')"
          :aria-label="t('settings.searchAria')"
          @keydown.esc="searchQuery = ''"
        />
      </div>
      <ul class="settings-nav-list">
        <li
          v-for="item in visiblePrimaryNavItems"
          :key="item.key"
          class="settings-nav-item"
        >
          <button
            type="button"
            class="settings-nav-btn"
            :class="{ 'is-active': activeSection === item.key }"
            :aria-label="t(item.labelKey)"
            :aria-current="activeSection === item.key ? 'page' : undefined"
            @click="selectSection(item.key)"
          >
            <span class="settings-nav-indicator" aria-hidden="true" />
            <span class="settings-nav-label">{{ t(item.labelKey) }}</span>
          </button>
        </li>
      </ul>
      <div v-if="visibleExperimentalNavItems.length" class="settings-nav-group">
        <p class="settings-nav-group-label">{{ t("settings.experimentalGroup") }}</p>
        <ul class="settings-nav-list">
          <li
            v-for="item in visibleExperimentalNavItems"
            :key="item.key"
            class="settings-nav-item"
          >
            <button
              type="button"
              class="settings-nav-btn settings-nav-btn--experimental"
              :class="{ 'is-active': activeSection === item.key }"
              :aria-label="t(item.labelKey)"
              :aria-current="activeSection === item.key ? 'page' : undefined"
              @click="selectSection(item.key)"
            >
              <span class="settings-nav-indicator" aria-hidden="true" />
              <span class="settings-nav-label">{{ t(item.labelKey) }}</span>
            </button>
          </li>
        </ul>
      </div>
    </aside>

    <main class="settings-content">
      <div
        v-if="searching"
        class="settings-search-results"
        role="list"
        :aria-label="t('settings.searchResultsAria')"
        aria-live="polite"
      >
        <button
          v-for="entry in searchResults"
          :key="`${entry.section}:${entry.labelKey}`"
          type="button"
          class="settings-search-result"
          role="listitem"
          @click="openSearchResult(entry)"
        >
          <span class="settings-search-result__label">{{ t(entry.labelKey) }}</span>
          <span class="settings-search-result__section">
            {{ t(allNavItems.find((item) => item.key === entry.section)?.labelKey ?? entry.labelKey) }}
          </span>
        </button>
        <p v-if="searchResults.length === 0" class="settings-search-results__empty" role="status">
          {{ t("settings.searchNoResults") }}
        </p>
      </div>

      <template v-else>
        <GeneralSection v-show="activeSection === 'general'" />
        <EditorSection v-show="activeSection === 'editor'" />
        <LspSection v-show="activeSection === 'lsp'" />
        <GitSection v-show="activeSection === 'git'" />
        <DebugSection v-show="activeSection === 'debug'" />
        <TerminalSection v-show="activeSection === 'terminal'" />
        <ShortcutsSection v-show="activeSection === 'shortcuts'" />
        <AppearanceSection v-show="activeSection === 'appearance'" />
        <ProfileSection v-show="activeSection === 'profiles'" />
        <LanguagePacksSection v-show="activeSection === 'languagePacks'" />
        <!-- GOAL-P1-07: AI sections (ai/agent/prompts/presets/computerUse) are no
             longer rendered here. Navigating to them redirects to the AI window.
             Only non-AI sections remain so there is exactly one writable instance
             of AI settings in the application. -->
      </template>
    </main>
  </div>
</template>

<style scoped>
.settings-view {
  position: relative;
  display: flex;
  height: 100%;
  overflow: hidden;
}

.settings-ai-window-error {
  position: absolute;
  z-index: 2;
  top: 12px;
  right: 16px;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  border: 1px solid var(--color-danger, #d92d20);
  border-radius: var(--radius-sm);
  color: var(--color-text-primary);
  background: var(--color-bg-surface);
  font-size: 12px;
}

.settings-ai-window-error button {
  border: 0;
  color: var(--color-primary);
  background: transparent;
  cursor: pointer;
}

.settings-nav {
  width: 200px;
  border-right: 1px solid var(--color-border-default);
  background: var(--color-bg-surface);
  padding: 16px 0;
  overflow-y: auto;
}

.settings-search {
  position: relative;
  margin: 0 12px 14px;
}

.settings-search__icon {
  position: absolute;
  top: 50%;
  left: 9px;
  z-index: 1;
  color: var(--color-text-tertiary);
  transform: translateY(-50%);
  pointer-events: none;
}

.settings-search__input {
  width: 100%;
  height: 32px;
  padding: 0 9px 0 30px;
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-sm);
  outline: none;
  color: var(--color-text-primary);
  background: var(--color-bg-base);
  font-size: 12px;
}

.settings-search__input:focus-visible {
  border-color: var(--color-primary);
  outline: var(--focus-ring-width, 2px) solid color-mix(in srgb, var(--color-primary) 40%, transparent);
  outline-offset: 1px;
}

.settings-nav-list {
  list-style: none;
  padding: 0;
  margin: 0;
}

.settings-nav-item {
  margin: 2px 8px;
}

.settings-nav-btn {
  position: relative;
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  text-align: left;
  padding: 8px 16px;
  border: none;
  background: transparent;
  color: var(--color-text-secondary);
  font-family: var(--font-sans);
  font-size: 13px;
  border-radius: var(--radius-sm);
  cursor: pointer;
  overflow: hidden;
  transition: background var(--transition-fast), color var(--transition-fast), transform 0.18s cubic-bezier(0.4, 0, 0.2, 1);
}

/* 左侧高亮指示条，宽度/opacity 过渡实现丝滑激活效果 */
.settings-nav-indicator {
  position: absolute;
  left: 0;
  top: 50%;
  transform: translateY(-50%) scaleY(0);
  width: 3px;
  height: 60%;
  border-radius: 0 3px 3px 0;
  background: var(--color-primary);
  opacity: 0;
  transition: transform 0.22s cubic-bezier(0.4, 0, 0.2, 1), opacity 0.22s ease;
}

.settings-nav-btn:hover {
  background: var(--color-sidebar-hover);
  color: var(--color-text-primary);
  transform: translateX(2px);
}

.settings-nav-btn:active {
  transform: translateX(0) scale(0.97);
}

.settings-nav-btn.is-active {
  background: var(--color-primary-container);
  color: var(--color-on-primary-container);
  font-weight: 500;
  transform: translateX(0);
}

.settings-nav-btn.is-active .settings-nav-indicator {
  transform: translateY(-50%) scaleY(1);
  opacity: 1;
}

/* prompt-6 Task 10: experimental settings group (no badge/recommendation). */
.settings-nav-group {
  margin-top: 16px;
  padding-top: 12px;
  border-top: 1px solid var(--color-border-default);
}

.settings-nav-group-label {
  margin: 0 16px 8px;
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.04em;
  text-transform: uppercase;
  color: var(--color-text-tertiary, var(--color-text-secondary));
  opacity: 0.85;
}

.settings-nav-btn--experimental {
  opacity: 0.9;
}

.settings-content {
  flex: 1;
  overflow-y: auto;
  padding: 24px 32px;
}

.settings-search-results {
  display: flex;
  flex-direction: column;
  max-width: 720px;
}

.settings-search-result {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 20px;
  min-height: 44px;
  padding: 9px 12px;
  border: 0;
  border-bottom: 1px solid var(--color-border-subtle);
  color: var(--color-text-primary);
  background: transparent;
  cursor: pointer;
  text-align: left;
}

.settings-search-result:hover,
.settings-search-result:focus-visible {
  background: var(--color-bg-surface-container-low);
}

.settings-search-result__label {
  min-width: 0;
  font-size: 13px;
}

.settings-search-result__section {
  flex: 0 0 auto;
  color: var(--color-text-tertiary);
  font-size: 11px;
}

.settings-search-results__empty {
  margin: 0;
  padding: 32px 12px;
  color: var(--color-text-tertiary);
  text-align: center;
}

.settings-content :deep(.settings-section) {
  max-width: 640px;
  /* 切换分区时淡入上移，实现丝滑过渡。
     v-show 从 display:none 变为 display:block 时 animation 自动触发。 */
  animation: settingsFadeInUp 0.28s cubic-bezier(0.4, 0, 0.2, 1);
}

@keyframes settingsFadeInUp {
  from {
    opacity: 0;
    transform: translateY(8px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}

/* 尊重用户的减少动效偏好 */
@media (prefers-reduced-motion: reduce) {
  .settings-content :deep(.settings-section) {
    animation: none;
  }
  .settings-nav-btn,
  .settings-nav-indicator {
    transition: none !important;
  }
}

.settings-content :deep(.section-title) {
  font-size: 18px;
  font-weight: 600;
  margin-bottom: 24px;
  color: var(--color-text-primary);
}

.settings-content :deep(.setting-row) {
  display: flex;
  align-items: center;
  gap: 16px;
  margin-bottom: 20px;
}

.settings-content :deep(.setting-label) {
  width: 180px;
  flex-shrink: 0;
  font-size: 13px;
  color: var(--color-text-secondary);
}

.settings-content :deep(.setting-control) {
  display: flex;
  align-items: center;
  gap: 8px;
}

.settings-content :deep(.slider-value) {
  font-size: 12px;
  color: var(--color-text-tertiary);
  margin-left: 8px;
}

.settings-content :deep(.prompt-actions) {
  display: flex;
  gap: 8px;
  margin-top: 8px;
  flex-wrap: wrap;
}

.settings-content :deep(.prompt-hint) {
  display: block;
  margin-top: 6px;
  font-size: 12px;
  color: var(--color-text-tertiary);
}

.settings-content :deep(.color-swatches) {
  display: flex;
  gap: 8px;
}

.settings-content :deep(.color-swatch) {
  width: 28px;
  height: 28px;
  border-radius: var(--radius-full);
  border: 2px solid transparent;
  cursor: pointer;
  transition: border-color var(--transition-fast), transform var(--transition-fast);
}

.settings-content :deep(.color-swatch:hover) {
  transform: scale(1.1);
}

.settings-content :deep(.color-swatch.is-selected) {
  border-color: var(--color-text-primary);
}

.settings-content :deep(.shortcut-key) {
  display: inline-block;
  padding: 2px 8px;
  background: var(--color-bg-surface-container);
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-xs);
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--color-text-primary);
}
</style>
