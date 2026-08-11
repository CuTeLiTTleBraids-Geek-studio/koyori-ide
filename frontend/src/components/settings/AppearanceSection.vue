<script setup lang="ts">
// Koyori IDE 组件 · Appearance Section。
// 喵，这是 Appearance Section，负责 Koyori IDE 的界面呈现喵~
import { ref, computed } from "vue";
import { ElMessageBox } from "element-plus";
import {
  appState,
  saveSettings,
  applyAccentTheme,
  applyMode,
  applyDesignLanguage,
  applyFontSizeScaling,
  applyUiDensity,
  setCustomAccent,
  serializeCustomAccent,
  deserializeCustomAccent,
} from "@/stores/app";
import { accentThemes } from "@/lib/monaco-themes";
import type { AccentTheme } from "@/lib/monaco-themes";
import type { CustomAccentTheme } from "@/types";
import { errorMessage } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";

const { t } = useI18n();

const accentColorList = computed(() =>
  Object.entries(accentThemes)
    .filter(([key]) => key !== "custom")
    .map(([key, meta]) => ({
      key: key as AccentTheme,
      label: meta.label,
      color: meta.color,
    })),
);

// Custom accent local form state (mirrors appState.customAccent for editing).
const customColor = ref(appState.customAccent?.color ?? "#ff6b35");
const customName = ref(appState.customAccent?.name ?? t("appearance.defaultCustomName"));

// Theme options for the card-style selector.
// Each card encodes BOTH a design language and a mode, so Claude is split
// into "Claude Light" and "Claude Dark" rather than being a separate toggle.
// Key format: "<designLanguage>:<mode>".
type ThemeCardKey = "apple:dark" | "apple:light" | "apple:system" | "claude:light" | "claude:dark";

const themeOptions = computed(() => [
  {
    key: "apple:light" as const,
    label: t("appearance.themeLight"),
    bg: "#ffffff",
    bar: "#f5f5f7",
    text: "#1d1d1f",
    accent: "#0066cc",
    fontClass: "",
  },
  {
    key: "apple:dark" as const,
    label: t("appearance.themeDark"),
    bg: "#1d1d1f",
    bar: "#272729",
    text: "#ffffff",
    accent: "#2997ff",
    fontClass: "",
  },
  {
    key: "apple:system" as const,
    label: t("appearance.themeSystem"),
    bg: "linear-gradient(135deg, #ffffff 0%, #ffffff 50%, #1d1d1f 50%, #1d1d1f 100%)",
    bar: "linear-gradient(135deg, #f5f5f7 0%, #f5f5f7 50%, #272729 50%, #272729 100%)",
    text: "#1d1d1f",
    accent: "#0066cc",
    fontClass: "",
  },
  {
    key: "claude:light" as const,
    label: t("appearance.themeClaudeLight"),
    bg: "#faf9f5",
    bar: "#f0ebe3",
    text: "#181715",
    accent: "#cc785c",
    fontClass: "preview-font-claude",
  },
  {
    key: "claude:dark" as const,
    label: t("appearance.themeClaudeDark"),
    bg: "#181715",
    bar: "#252320",
    text: "#faf9f5",
    accent: "#d4926f",
    fontClass: "preview-font-claude",
  },
]);

function isThemeCardSelected(key: ThemeCardKey): boolean {
  const [lang, mode] = key.split(":") as ["apple" | "claude", "dark" | "light" | "system"];
  if (lang !== appState.designLanguage) return false;
  // Claude cards always set a concrete mode (light/dark) in handleThemeChange,
  // so a simple equality check suffices.
  return appState.theme === mode;
}

function handleThemeChange(key: ThemeCardKey) {
  const [lang, mode] = key.split(":") as ["apple" | "claude", "dark" | "light" | "system"];
  applyDesignLanguage(lang);
  appState.theme = mode;
  applyMode(mode);
  saveSettings();
}

function handleFontSizeScalingChange(value: number | number[]) {
  // el-slider emits number | number[] (range mode); we use single-thumb.
  const scale = Array.isArray(value) ? value[0] : value;
  applyFontSizeScaling(scale);
  saveSettings();
}

function handleUiDensityChange(value: string | number | boolean | undefined) {
  applyUiDensity(String(value ?? "comfortable"));
  saveSettings();
}

function selectAccent(key: AccentTheme) {
  applyAccentTheme(key);
  saveSettings();
}

function applyCustomAccent() {
  const custom: CustomAccentTheme = {
    name: customName.value || t("appearance.customDefault"),
    color: customColor.value,
  };
  setCustomAccent(custom);
}

function exportTheme() {
  if (!appState.customAccent) return;
  const json = serializeCustomAccent(appState.customAccent);
  const blob = new Blob([json], { type: "application/json" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `${appState.customAccent.name.replace(/\s+/g, "-").toLowerCase()}.theme.json`;
  a.click();
  URL.revokeObjectURL(url);
}

function importTheme() {
  const input = document.createElement("input");
  input.type = "file";
  input.accept = ".json,application/json";
  input.onchange = () => {
    const file = input.files?.[0];
    if (!file) return;
    const reader = new FileReader();
    reader.onload = () => {
      try {
        const text = reader.result as string;
        const custom = deserializeCustomAccent(text);
        customColor.value = custom.color;
        customName.value = custom.name;
        setCustomAccent(custom);
      } catch (e) {
        void ElMessageBox.alert(errorMessage(e), t("appearance.importFailed"), { type: "error" });
      }
    };
    reader.readAsText(file);
  };
  input.click();
}
</script>

<template>
  <section class="settings-section">
    <h2 class="section-title">{{ t("appearance.title") }}</h2>

    <!-- ── Theme (Dark / Light / System / Claude) ── -->
    <div class="theme-block">
      <div class="block-header">
        <label class="setting-label">{{ t("appearance.theme") }}</label>
        <p class="block-hint">{{ t("appearance.designLanguageHint") }}</p>
      </div>
      <div
        class="theme-cards"
        role="radiogroup"
        :aria-label="t('appearance.themeAria')"
      >
        <button
          v-for="opt in themeOptions"
          :key="opt.key"
          type="button"
          class="theme-card"
          :class="[
            { 'is-selected': isThemeCardSelected(opt.key) },
            opt.fontClass,
          ]"
          role="radio"
          :aria-checked="isThemeCardSelected(opt.key)"
          :aria-label="opt.label"
          @click="handleThemeChange(opt.key)"
        >
          <div class="theme-card__preview" :style="{ background: opt.bg }">
            <div class="theme-preview__titlebar">
              <span
                class="theme-dot"
                :style="{ background: opt.accent }"
              />
              <span
                class="theme-dot"
                :style="{ background: opt.accent, opacity: 0.55 }"
              />
              <span
                class="theme-dot"
                :style="{ background: opt.accent, opacity: 0.3 }"
              />
            </div>
            <div class="theme-preview__bar" :style="{ background: opt.bar }" />
            <div class="theme-preview__lines">
              <span :style="{ background: opt.text, opacity: 0.85 }" />
              <span :style="{ background: opt.text, opacity: 0.55 }" />
              <span :style="{ background: opt.text, opacity: 0.35 }" />
            </div>
            <div
              class="theme-preview__cta"
              :style="{ background: opt.accent }"
            >
              Aa
            </div>
          </div>
          <div class="theme-card__label">{{ opt.label }}</div>
          <div
            v-if="isThemeCardSelected(opt.key)"
            class="theme-card__check"
            :style="{ borderColor: opt.accent, color: opt.accent }"
          >
            <svg viewBox="0 0 16 16" width="14" height="14">
              <path
                fill="currentColor"
                d="M13.485 4.485a1 1 0 0 1 0 1.415l-6.5 6.5a1 1 0 0 1-1.414 0l-3-3a1 1 0 1 1 1.414-1.414L6.428 10.5l5.643-5.643a1 1 0 0 1 1.414 0z"
              />
            </svg>
          </div>
        </button>
      </div>
    </div>

    <!-- ── Color Accent ── -->
    <div class="setting-block">
      <label class="setting-label">{{ t("appearance.colorAccent") }}</label>
      <div class="setting-control">
        <div class="color-swatches">
          <button
            type="button"
            v-for="item in accentColorList"
            :key="item.key"
            class="color-swatch"
            :class="{ 'is-selected': appState.accentTheme === item.key }"
            :style="{ backgroundColor: item.color }"
            :aria-label="t('appearance.selectAccentColor', { name: item.label })"
            :aria-pressed="appState.accentTheme === item.key"
            @click="selectAccent(item.key)"
          />
          <button
            type="button"
            class="color-swatch color-swatch--custom"
            :class="{ 'is-selected': appState.accentTheme === 'custom' }"
            :style="{ backgroundColor: customColor }"
            :aria-label="t('appearance.selectCustomAccentColor')"
            :aria-pressed="appState.accentTheme === 'custom'"
            @click="selectAccent('custom')"
          />
        </div>
      </div>
    </div>

    <!-- Plan 48: Custom accent editor -->
    <div v-if="appState.accentTheme === 'custom'" class="setting-block setting-block--indent">
      <label class="setting-label">{{ t("appearance.customAccent") }}</label>
      <div class="setting-control">
        <div class="custom-accent-editor">
          <div class="custom-accent-row">
            <label class="custom-accent-label">{{ t("appearance.customName") }}</label>
            <el-input
              v-model="customName"
              size="default"
              style="width: var(--setting-control-width-sm, 200px)"
              :aria-label="t('appearance.customThemeNameAria')"
            />
          </div>
          <div class="custom-accent-row">
            <label class="custom-accent-label">{{ t("appearance.customColorLabel") }}</label>
            <el-color-picker
              v-model="customColor"
              :aria-label="t('appearance.customAccentColorAria')"
            />
            <span class="custom-accent-hex">{{ customColor }}</span>
          </div>
          <div class="custom-accent-actions">
            <el-button size="default" type="primary" @click="applyCustomAccent">
              {{ t("appearance.apply") }}
            </el-button>
            <el-button size="default" @click="exportTheme" :disabled="!appState.customAccent">
              {{ t("appearance.export") }}
            </el-button>
            <el-button size="default" @click="importTheme">
              {{ t("appearance.import") }}
            </el-button>
          </div>
        </div>
      </div>
    </div>

    <!-- ── Font Size Scaling ── -->
    <div class="setting-block">
      <label class="setting-label">{{ t("appearance.fontSizeScaling") }}</label>
      <div class="setting-control">
        <el-slider
          v-model="appState.fontSizeScaling"
          :min="80"
          :max="150"
          :step="5"
          style="width: var(--setting-control-width)"
          :aria-label="t('appearance.fontSizeScalingAria')"
          @change="handleFontSizeScalingChange"
        />
        <span class="slider-value">{{ appState.fontSizeScaling }}%</span>
      </div>
    </div>

    <!-- ── UI Density ── -->
    <div class="setting-block">
      <label class="setting-label">{{ t("appearance.uiDensity") }}</label>
      <div class="setting-control">
        <el-select
          v-model="appState.uiDensity"
          size="default"
          style="width: var(--setting-control-width-sm)"
          :aria-label="t('appearance.uiDensityAria')"
          @change="handleUiDensityChange"
        >
          <el-option :label="t('appearance.densityCompact')" value="compact" />
          <el-option :label="t('appearance.densityComfortable')" value="comfortable" />
          <el-option :label="t('appearance.densitySpacious')" value="spacious" />
        </el-select>
      </div>
    </div>
  </section>
</template>

<style scoped>
/* ── Theme block (Dark / Light / System / Claude) ── */
.theme-block {
  margin-bottom: 24px;
}

.block-header {
  margin-bottom: 12px;
}

.block-hint {
  margin-top: 4px;
  font-size: 12px;
  line-height: 1.4;
  color: var(--color-text-tertiary);
  max-width: 520px;
}

.theme-cards {
  display: grid;
  grid-template-columns: repeat(5, 1fr);
  gap: 10px;
  max-width: 760px;
}

.theme-card {
  position: relative;
  display: flex;
  flex-direction: column;
  padding: 0;
  background: var(--color-bg-surface);
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-sm);
  cursor: pointer;
  overflow: hidden;
  transition: border-color var(--transition-fast),
              box-shadow var(--transition-fast),
              transform var(--duration-fast) var(--ease-standard);
  text-align: left;
  font-family: inherit;
  color: inherit;
}

.theme-card:hover {
  border-color: var(--color-border-strong);
  box-shadow: var(--shadow-floating);
}

.theme-card:active {
  transform: scale(0.97);
}

.theme-card.is-selected {
  border-color: var(--color-primary);
  border-width: 2px;
  box-shadow: 0 0 0 3px rgba(0, 102, 204, 0.12);
}

/* Claude card uses coral accent for the selection ring */
.theme-card.preview-font-claude.is-selected {
  border-color: #cc785c;
  box-shadow: 0 0 0 3px rgba(204, 120, 92, 0.15);
}

.theme-card__preview {
  height: 96px;
  padding: 8px;
  display: flex;
  flex-direction: column;
  gap: 6px;
  border-bottom: 1px solid var(--color-border-subtle);
  position: relative;
}

.theme-preview__titlebar {
  display: flex;
  gap: 5px;
  margin-bottom: 2px;
}

.theme-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
}

.theme-preview__bar {
  height: 8px;
  border-radius: 3px;
}

.theme-preview__lines {
  display: flex;
  flex-direction: column;
  gap: 4px;
  flex: 1;
  justify-content: center;
}

.theme-preview__lines span {
  display: block;
  height: 4px;
  border-radius: 2px;
}

.theme-preview__lines span:nth-child(1) { width: 75%; }
.theme-preview__lines span:nth-child(2) { width: 55%; }
.theme-preview__lines span:nth-child(3) { width: 35%; }

.theme-preview__cta {
  position: absolute;
  bottom: 8px;
  right: 8px;
  width: 26px;
  height: 26px;
  border-radius: 7px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 11px;
  font-weight: 600;
  color: #ffffff;
  font-family: "SF Pro Text", "Inter", -apple-system, system-ui, sans-serif;
}

.theme-card.preview-font-claude .theme-preview__cta {
  font-family: "StyreneB", "Inter", Georgia, serif;
  border-radius: 8px;
}

.theme-card__label {
  padding: 7px 8px;
  font-size: 12px;
  font-weight: 500;
  text-align: center;
  color: var(--color-text-secondary);
}

.theme-card.is-selected .theme-card__label {
  color: var(--color-primary);
  font-weight: 600;
}

.theme-card.preview-font-claude.is-selected .theme-card__label {
  color: #cc785c;
}

.theme-card__check {
  position: absolute;
  top: 6px;
  right: 6px;
  width: 20px;
  height: 20px;
  border-radius: 50%;
  background: var(--color-bg-surface);
  border: 2px solid var(--color-primary);
  display: flex;
  align-items: center;
  justify-content: center;
}

/* ── Setting block layout (shared) ── */
.setting-block {
  display: grid;
  grid-template-columns: 180px 1fr;
  gap: 16px;
  align-items: start;
  margin-bottom: 16px;
}

.setting-block--indent {
  padding-left: 24px;
}

.setting-label {
  font-size: 14px;
  font-weight: 500;
  color: var(--color-text-primary);
  padding-top: 6px;
}

.setting-control {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}

/* ── Custom accent editor ── */
.custom-accent-editor {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 12px;
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-sm);
  background: var(--color-bg-surface-container-low);
}

.custom-accent-row {
  display: flex;
  align-items: center;
  gap: 12px;
}

.custom-accent-label {
  width: 60px;
  font-size: 13px;
  color: var(--color-text-secondary);
}

.custom-accent-hex {
  font-family: var(--font-mono);
  font-size: 13px;
  color: var(--color-text-secondary);
}

.custom-accent-actions {
  display: flex;
  gap: 8px;
}

/* ── Color swatches ── */
.color-swatches {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}

.color-swatch {
  width: 28px;
  height: 28px;
  border-radius: 50%;
  border: 2px solid transparent;
  cursor: pointer;
  padding: 0;
  transition: transform var(--duration-fast) var(--ease-standard),
              border-color var(--transition-fast);
}

.color-swatch:hover {
  transform: scale(1.1);
}

.color-swatch.is-selected {
  border-color: var(--color-text-primary);
  transform: scale(1.1);
}

.color-swatch--custom {
  position: relative;
  background-image: linear-gradient(45deg, #555 25%, transparent 25%, transparent 75%, #555 75%, #555),
                     linear-gradient(45deg, #555 25%, transparent 25%, transparent 75%, #555 75%, #555);
  background-size: 8px 8px;
  background-position: 0 0, 4px 4px;
}

.color-swatch--custom::after {
  content: "+";
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  color: white;
  font-weight: bold;
  font-size: 16px;
  text-shadow: 0 0 3px rgba(0, 0, 0, 0.8);
}

.slider-value {
  font-family: var(--font-mono);
  font-size: 13px;
  color: var(--color-text-secondary);
  min-width: 48px;
}
</style>
