<script setup lang="ts">
// Koyori IDE 组件 · Ai Window Theme Picker。
// 喵，这是 Ai Window Theme Picker，负责 Koyori IDE 的界面呈现喵~
import { useI18n } from "@/lib/i18n";
import type { AIWindowTheme } from "@/stores/aiWindow";

defineProps<{ theme: AIWindowTheme }>();
const emit = defineEmits<{ (e: "update:theme", theme: AIWindowTheme): void }>();
const { t } = useI18n();

const themes: Array<{
  value: AIWindowTheme;
  labelKey: string;
  surface: string;
  panel: string;
  accent: string;
  text: string;
  claude?: boolean;
}> = [
  { value: "apple-dark", labelKey: "aiWorkspace.themeAppleDark", surface: "#1d1d1f", panel: "#2a2a2c", accent: "#2997ff", text: "#f5f5f7" },
  { value: "apple-light", labelKey: "aiWorkspace.themeAppleLight", surface: "#f5f5f7", panel: "#ffffff", accent: "#0066cc", text: "#1d1d1f" },
  { value: "claude-dark", labelKey: "aiWorkspace.themeClaudeDark", surface: "#181715", panel: "#252320", accent: "#cc785c", text: "#faf9f5", claude: true },
  { value: "claude-light", labelKey: "aiWorkspace.themeClaudeLight", surface: "#faf9f5", panel: "#efe9de", accent: "#cc785c", text: "#141413", claude: true },
  { value: "system", labelKey: "aiWorkspace.themeSystem", surface: "linear-gradient(135deg,#f5f5f7 50%,#1d1d1f 50%)", panel: "rgba(255,255,255,.7)", accent: "#0066cc", text: "#555" },
];
</script>

<template>
  <div class="ai-theme-picker" role="radiogroup" :aria-label="t('aiWorkspace.windowTheme')">
    <button
      v-for="item in themes"
      :key="item.value"
      type="button"
      role="radio"
      class="ai-theme-picker__card"
      :class="{ 'is-selected': theme === item.value, 'is-claude': item.claude }"
      :data-theme="item.value"
      :aria-checked="theme === item.value"
      @click="emit('update:theme', item.value)"
    >
      <span class="ai-theme-picker__preview" :style="{ background: item.surface, color: item.text }">
        <span class="ai-theme-picker__rail" :style="{ background: item.panel }" />
        <span class="ai-theme-picker__line" :style="{ background: item.text }" />
        <span class="ai-theme-picker__line is-short" :style="{ background: item.text }" />
        <span class="ai-theme-picker__accent" :style="{ background: item.accent }" />
      </span>
      <span>{{ t(item.labelKey) }}</span>
    </button>
  </div>
</template>

<style scoped>
.ai-theme-picker { display: grid; grid-template-columns: repeat(5, minmax(104px, 1fr)); gap: 10px; }
.ai-theme-picker__card { display: grid; gap: 7px; padding: 7px; border: 1px solid var(--color-border-default); border-radius: var(--radius-sm); color: var(--color-text-secondary); background: var(--color-bg-surface); font: inherit; font-size: 11px; text-align: center; cursor: pointer; transition: border-color var(--transition-fast), transform var(--transition-fast); }
.ai-theme-picker__card:hover { border-color: var(--color-border-strong); }
.ai-theme-picker__card:active { transform: scale(.97); }
.ai-theme-picker__card.is-selected { color: var(--color-primary); border-color: var(--color-primary); }
.ai-theme-picker__card.is-claude.is-selected { color: #cc785c; border-color: #cc785c; }
.ai-theme-picker__preview { position: relative; display: block; height: 70px; overflow: hidden; border-radius: 6px; }
.ai-theme-picker__rail { position: absolute; inset: 0 auto 0 0; width: 28%; opacity: .9; }
.ai-theme-picker__line { position: absolute; left: 38%; top: 25%; width: 48%; height: 4px; border-radius: 4px; opacity: .7; }
.ai-theme-picker__line.is-short { top: 43%; width: 34%; opacity: .4; }
.ai-theme-picker__accent { position: absolute; right: 12%; bottom: 14%; width: 24px; height: 16px; border-radius: 999px; }
@media (max-width: 900px) { .ai-theme-picker { grid-template-columns: repeat(2, minmax(120px, 1fr)); } }
@media (prefers-reduced-motion: reduce) { .ai-theme-picker__card { transition: none; } .ai-theme-picker__card:active { transform: none; } }
</style>
