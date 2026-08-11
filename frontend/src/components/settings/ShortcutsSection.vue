<script setup lang="ts">
// Koyori IDE 组件 · Shortcuts Section。
// 喵，这是 Shortcuts Section，负责 Koyori IDE 的界面呈现喵~
import { ref, onMounted, onUnmounted, computed } from "vue";
import {
  listShortcuts,
  setCustomShortcut,
  resetCustomShortcut,
  resetAllCustomShortcuts,
  findConflicts,
  formatShortcutKey,
} from "@/composables/useKeyboard";
import { saveSettings } from "@/stores/app";
import { ElMessage, ElMessageBox } from "element-plus";
import type { ShortcutKeys } from "@/types";
import { useI18n } from "@/lib/i18n";

const { t } = useI18n();

// Read live shortcuts from the keyboard composable (#25 / N-8). This stays
// in sync with actual registerShortcut() calls in MainLayout.vue, avoiding
// drift between the hardcoded list and real bindings.
interface ShortcutRow {
  label: string;
  keys: string;
  isCustom: boolean;
  defaultKeys: string;
}

const shortcuts = ref<ShortcutRow[]>([]);
// Label of the row currently capturing a keystroke, or null when idle.
const capturingLabel = ref<string | null>(null);
// Live preview of the keystroke being captured.
const capturingPreview = ref<string>("");
// Conflict warning for the in-progress capture.
const capturingConflict = ref<string[]>([]);
// Most recent captured ShortcutKeys, stashed so confirmCapture can apply it.
let lastCapturedKeys: ShortcutKeys | null = null;

function refresh(): void {
  shortcuts.value = listShortcuts();
}

function isModifierKey(key: string): boolean {
  return (
    key === "Shift" || key === "Control" || key === "Alt" || key === "Meta"
  );
}

function handleCapture(e: KeyboardEvent): void {
  if (!capturingLabel.value) return;
  // Always swallow keys while capturing so the app's shortcuts don't fire.
  e.preventDefault();
  e.stopPropagation();
  // Escape cancels.
  if (e.key === "Escape") {
    cancelCapture();
    return;
  }
  // Enter confirms the current preview (if any).
  if (e.key === "Enter") {
    if (capturingPreview.value && lastCapturedKeys) {
      confirmCapture();
    }
    return;
  }
  // Ignore bare modifier presses — wait for the actual key.
  if (isModifierKey(e.key)) return;
  const keys: ShortcutKeys = {
    key: e.key,
    ctrl: e.ctrlKey || e.metaKey,
    shift: e.shiftKey,
    alt: e.altKey,
  };
  lastCapturedKeys = keys;
  capturingPreview.value = formatShortcutKey(keys);
  capturingConflict.value = findConflicts(capturingLabel.value, keys);
}

function startCapture(label: string): void {
  capturingLabel.value = label;
  capturingPreview.value = "";
  capturingConflict.value = [];
  lastCapturedKeys = null;
}

function cancelCapture(): void {
  capturingLabel.value = null;
  capturingPreview.value = "";
  capturingConflict.value = [];
  lastCapturedKeys = null;
}

function confirmCapture(): void {
  const label = capturingLabel.value;
  const keys = lastCapturedKeys;
  if (!label || !keys) {
    cancelCapture();
    return;
  }
  setCustomShortcut(label, keys);
  saveSettings();
  cancelCapture();
  refresh();
}

function handleReset(label: string): void {
  resetCustomShortcut(label);
  saveSettings();
  refresh();
  ElMessage.success(t("shortcuts.resetSuccess", { label }));
}

async function handleResetAll(): Promise<void> {
  try {
    await ElMessageBox.confirm(
      t("shortcuts.resetAllConfirm"),
      t("shortcuts.resetAllTitle"),
      { confirmButtonText: t("shortcuts.resetAll"), cancelButtonText: t("common.cancel"), type: "warning" },
    );
  } catch {
    return;
  }
  resetAllCustomShortcuts();
  saveSettings();
  refresh();
  ElMessage.success(t("shortcuts.allReset"));
}

const hasCustomizations = computed(() => shortcuts.value.some((s) => s.isCustom));

onMounted(() => {
  refresh();
  window.addEventListener("keydown", handleCapture, true);
});

onUnmounted(() => {
  window.removeEventListener("keydown", handleCapture, true);
});
</script>

<template>
  <section class="settings-section">
    <div class="section-header">
      <h2 class="section-title">{{ t("shortcuts.title") }}</h2>
      <button
        type="button"
        v-if="hasCustomizations"
        class="shortcuts__reset-all"
        @click="handleResetAll"
      >
        {{ t("shortcuts.resetAll") }}
      </button>
    </div>

    <p class="shortcuts__hint">
      {{ t("shortcuts.hint") }}
    </p>

    <div v-for="s in shortcuts" :key="s.label" class="setting-row shortcuts__row">
      <label class="setting-label">{{ s.label }}</label>
      <div class="setting-control shortcuts__control">
        <template v-if="capturingLabel === s.label">
          <kbd class="shortcut-key shortcut-key--capturing">
            {{ capturingPreview || t("shortcuts.pressKeys") }}
          </kbd>
          <span
            v-if="capturingConflict.length > 0"
            class="shortcuts__conflict"
            :title="t('shortcuts.conflictsTitle', { names: capturingConflict.join(', ') })"
          >
            {{ t("shortcuts.conflictsWith", { names: capturingConflict.join(", ") }) }}
          </span>
          <button type="button" class="shortcuts__btn shortcuts__btn--confirm" @click="confirmCapture">
            {{ t("shortcuts.confirm") }}
          </button>
          <button type="button" class="shortcuts__btn shortcuts__btn--cancel" @click="cancelCapture">
            {{ t("common.cancel") }}
          </button>
        </template>
        <template v-else>
          <button
            type="button"
            class="shortcut-key shortcuts__key-btn"
            :class="{ 'shortcut-key--custom': s.isCustom }"
            :title="s.isCustom ? t('shortcuts.defaultTitle', { keys: s.defaultKeys }) : t('shortcuts.clickToChange')"
            @click="startCapture(s.label)"
          >
            {{ s.keys }}
          </button>
          <span v-if="s.isCustom" class="shortcuts__custom-badge">{{ t("shortcuts.customBadge") }}</span>
          <button
            type="button"
            v-if="s.isCustom"
            class="shortcuts__btn shortcuts__btn--reset"
            :title="t('shortcuts.resetToTitle', { keys: s.defaultKeys })"
            @click="handleReset(s.label)"
          >
            {{ t("shortcuts.reset") }}
          </button>
        </template>
      </div>
    </div>
  </section>
</template>

<style scoped>
.settings-section {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.section-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.section-title {
  margin: 0;
  font-size: 14px;
  font-weight: 600;
  color: var(--color-text-primary);
}

.shortcuts__reset-all {
  background: transparent;
  border: 1px solid var(--color-border-subtle);
  color: var(--color-text-secondary);
  font-size: 11px;
  padding: 3px 8px;
  border-radius: var(--radius-sm);
  cursor: pointer;
}

.shortcuts__reset-all:hover {
  background: var(--color-bg-surface-container-low);
  color: var(--color-text-primary);
}

.shortcuts__hint {
  margin: 0;
  font-size: 11px;
  color: var(--color-text-tertiary);
}

.shortcuts__row {
  align-items: center;
}

.shortcuts__control {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.shortcut-key {
  display: inline-flex;
  align-items: center;
  min-width: 28px;
  padding: 2px 8px;
  font-family: var(--font-mono, monospace);
  font-size: 11px;
  background: var(--color-bg-surface-container-low);
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-sm);
  color: var(--color-text-primary);
}

.shortcut-key--capturing {
  border-style: dashed;
  border-color: var(--color-accent, #4285f4);
  color: var(--color-text-secondary);
}

.shortcuts__key-btn {
  cursor: pointer;
  transition: border-color var(--transition-fast);
}

.shortcuts__key-btn:hover {
  border-color: var(--color-accent, #4285f4);
}

.shortcut-key--custom {
  border-color: var(--color-accent, #4285f4);
  color: var(--color-accent, #4285f4);
}

.shortcuts__custom-badge {
  font-size: 10px;
  color: var(--color-text-tertiary);
  text-transform: uppercase;
  letter-spacing: 0.5px;
}

.shortcuts__conflict {
  font-size: 11px;
  color: var(--color-warning, #f5a623);
}

.shortcuts__btn {
  background: transparent;
  border: 1px solid var(--color-border-subtle);
  color: var(--color-text-secondary);
  font-size: 11px;
  padding: 2px 8px;
  border-radius: var(--radius-sm);
  cursor: pointer;
}

.shortcuts__btn:hover {
  background: var(--color-bg-surface-container-low);
  color: var(--color-text-primary);
}

.shortcuts__btn--confirm {
  border-color: var(--color-accent, #4285f4);
  color: var(--color-accent, #4285f4);
}
</style>
