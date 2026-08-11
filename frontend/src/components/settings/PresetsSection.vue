<script setup lang="ts">
// Koyori IDE 组件 · Presets Section。
// 喵，这是 Presets Section，负责 Koyori IDE 的界面呈现喵~
import { onMounted, ref, computed } from "vue";
import { ElMessageBox } from "element-plus";
import {
  presetsState,
  loadPresets,
  saveProjectPreset,
  saveUserPreset,
  deleteProjectPreset,
  deleteUserPreset,
} from "@/stores/presets";
import type { PresetFile, PresetWithSource } from "@/types";
import { useI18n } from "@/lib/i18n";
import FocusTrapDialog from "@/components/common/FocusTrapDialog.vue";

const { t } = useI18n();

// Editor state
const showEditor = ref(false);
const editingPreset = ref<PresetFile>({
  name: "",
  label: "",
  description: "",
  prompt: "",
});
const editorTarget = ref<"project" | "user">("project");
const editorError = ref<string | null>(null);

const sourceBadgeColor = (source: string): string => {
  switch (source) {
    case "builtin": return "var(--color-success, #4caf50)";
    case "user": return "var(--color-info, #2196f3)";
    case "project": return "var(--color-warning, #ff9800)";
    default: return "var(--color-text-tertiary)";
  }
};

const sortedPresets = computed(() => presetsState.presetsWithSource);

onMounted(() => {
  void loadPresets();
});

function openNewPreset(target: "project" | "user") {
  editingPreset.value = {
    name: "",
    label: "",
    description: "",
    prompt: "",
  };
  editorTarget.value = target;
  editorError.value = null;
  showEditor.value = true;
}

function openEditPreset(p: PresetWithSource) {
  // Only project and user presets can be edited (builtin is read-only).
  if (p.source === "builtin") {
    // Clone as a new project preset with the same content.
    editingPreset.value = {
      name: p.name,
      label: p.label,
      description: p.description,
      icon: p.icon,
      prompt: p.prompt,
    };
    editorTarget.value = "project";
  } else {
    editingPreset.value = {
      name: p.name,
      label: p.label,
      description: p.description,
      icon: p.icon,
      prompt: p.prompt,
    };
    editorTarget.value = p.source as "project" | "user";
  }
  editorError.value = null;
  showEditor.value = true;
}

async function savePreset() {
  if (!editingPreset.value.name.trim()) {
    editorError.value = t("presets.errorNameRequired");
    return;
  }
  if (!editingPreset.value.prompt.trim()) {
    editorError.value = t("presets.errorPromptRequired");
    return;
  }
  try {
    if (editorTarget.value === "project") {
      await saveProjectPreset(editingPreset.value);
    } else {
      await saveUserPreset(editingPreset.value);
    }
    showEditor.value = false;
  } catch (e: unknown) {
    editorError.value = e instanceof Error ? e.message : String(e);
  }
}

async function removePreset(p: PresetWithSource) {
  if (p.source === "builtin") return;
  try {
    await ElMessageBox.confirm(
      t("presets.deleteConfirm", { name: p.name }),
      t("common.confirm"),
      { type: "warning", confirmButtonText: t("common.confirm"), cancelButtonText: t("common.cancel") },
    );
  } catch {
    return;
  }
  try {
    if (p.source === "project") {
      await deleteProjectPreset(p.name);
    } else {
      await deleteUserPreset(p.name);
    }
  } catch {
    // Error already notified by the store.
  }
}

function cancelEditor() {
  showEditor.value = false;
  editorError.value = null;
}
</script>

<template>
  <div class="settings-section presets-section">
    <h2 class="section-title">{{ t("presets.title") }}</h2>
    <p class="section-hint">
      {{ t("presets.hint") }}
    </p>

    <div class="preset-actions">
      <button type="button" class="preset-btn" @click="openNewPreset('project')">
        {{ t("presets.newProjectPreset") }}
      </button>
      <button type="button" class="preset-btn" @click="openNewPreset('user')">
        {{ t("presets.newUserPreset") }}
      </button>
      <button type="button" class="preset-btn preset-btn--refresh" @click="loadPresets()" :disabled="presetsState.loading">
        {{ presetsState.loading ? t("presets.loading") : t("presets.refresh") }}
      </button>
    </div>

    <div v-if="presetsState.error" class="preset-error">
      {{ t("presets.errorPrefix", { error: presetsState.error }) }}
    </div>

    <ul class="preset-list">
      <li v-for="p in sortedPresets" :key="p.name" class="preset-item">
        <div class="preset-item__header">
          <span class="preset-item__name">{{ p.label || p.name }}</span>
          <span
            class="preset-item__badge"
            :style="{ backgroundColor: sourceBadgeColor(p.source) }"
          >
            {{ p.source }}
          </span>
        </div>
        <div class="preset-item__meta">
          <code>{{ p.name }}</code>
          <span v-if="p.description" class="preset-item__desc">— {{ p.description }}</span>
        </div>
        <div class="preset-item__actions">
          <button type="button" class="preset-btn preset-btn--sm" @click="openEditPreset(p)">
            {{ p.source === 'builtin' ? t("presets.clone") : t("presets.edit") }}
          </button>
          <button
            type="button"
            v-if="p.source !== 'builtin'"
            class="preset-btn preset-btn--sm preset-btn--danger"
            @click="removePreset(p)"
          >
            {{ t("common.delete") }}
          </button>
        </div>
      </li>
    </ul>

    <div
      v-if="showEditor"
      class="preset-editor-overlay"
    >
      <button
        type="button"
        class="dialog-backdrop-button"
        tabindex="-1"
        :aria-label="t('a11y.closeDialog')"
        @click="cancelEditor"
      />
      <FocusTrapDialog
        class="preset-editor"
        :aria-label="editingPreset.name ? t('presets.edit') : t('presets.newTitle')"
        @close="cancelEditor"
      >
        <h3 class="preset-editor__title">
          {{ editingPreset.name ? t("presets.edit") : t("presets.newTitle") }} {{ editorTarget === 'project' ? t("presets.targetProject") : t("presets.targetUser") }} {{ t("presets.preset") }}
        </h3>
        <div v-if="editorError" class="preset-error">{{ editorError }}</div>
        <label class="preset-editor__field">
          <span>{{ t("presets.nameLabel") }}</span>
          <input
            v-model="editingPreset.name"
            type="text"
            :placeholder="t('presets.namePlaceholder')"
            class="preset-editor__input"
          />
        </label>
        <label class="preset-editor__field">
          <span>{{ t("presets.labelField") }}</span>
          <input
            v-model="editingPreset.label"
            type="text"
            :placeholder="t('presets.labelPlaceholder')"
            class="preset-editor__input"
          />
        </label>
        <label class="preset-editor__field">
          <span>{{ t("presets.description") }}</span>
          <input
            v-model="editingPreset.description"
            type="text"
            :placeholder="t('presets.descriptionPlaceholder')"
            class="preset-editor__input"
          />
        </label>
        <label class="preset-editor__field">
          <span>{{ t("presets.promptLabel") }}</span>
          <textarea
            v-model="editingPreset.prompt"
            rows="8"
            :placeholder="t('presets.promptPlaceholder')"
            class="preset-editor__textarea"
          ></textarea>
        </label>
        <div class="preset-editor__actions">
          <button type="button" class="preset-btn" @click="cancelEditor">{{ t("common.cancel") }}</button>
          <button type="button" class="preset-btn preset-btn--primary" @click="savePreset">{{ t("common.save") }}</button>
        </div>
      </FocusTrapDialog>
    </div>
  </div>
</template>

<style scoped>
.presets-section {
  max-width: 720px;
}

.section-hint {
  font-size: 13px;
  color: var(--color-text-secondary);
  margin-bottom: 16px;
  line-height: 1.5;
}

.preset-actions {
  display: flex;
  gap: 8px;
  margin-bottom: 16px;
  flex-wrap: wrap;
}

.preset-btn {
  padding: 6px 12px;
  border: 1px solid var(--color-border-default);
  background: var(--color-bg-surface);
  color: var(--color-text-primary);
  font-family: var(--font-sans);
  font-size: 13px;
  border-radius: var(--radius-sm);
  cursor: pointer;
  transition: background var(--transition-fast);
}

.preset-btn:hover:not(:disabled) {
  background: var(--color-sidebar-hover);
}

.preset-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.preset-btn--sm {
  padding: 3px 8px;
  font-size: 12px;
}

.preset-btn--primary {
  background: var(--color-primary);
  color: var(--color-on-primary);
  border-color: var(--color-primary);
}

.preset-btn--danger {
  color: var(--color-error, #f44336);
  border-color: var(--color-error, #f44336);
}

.preset-btn--refresh {
  margin-left: auto;
}

.preset-error {
  color: var(--color-error, #f44336);
  font-size: 13px;
  margin: 8px 0;
  padding: 8px 12px;
  background: var(--color-error-container, rgba(244, 67, 54, 0.1));
  border-radius: var(--radius-sm);
}

.preset-list {
  list-style: none;
  padding: 0;
  margin: 0;
}

.preset-item {
  padding: 12px;
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-sm);
  margin-bottom: 8px;
  background: var(--color-bg-surface);
}

.preset-item__header {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 4px;
}

.preset-item__name {
  font-weight: 500;
  font-size: 14px;
  color: var(--color-text-primary);
}

.preset-item__badge {
  font-size: 10px;
  padding: 2px 6px;
  border-radius: var(--radius-xs);
  color: white;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}

.preset-item__meta {
  font-size: 12px;
  color: var(--color-text-secondary);
  margin-bottom: 8px;
}

.preset-item__meta code {
  font-family: var(--font-mono);
  background: var(--color-bg-surface-container);
  padding: 1px 4px;
  border-radius: var(--radius-xs);
}

.preset-item__desc {
  color: var(--color-text-tertiary);
}

.preset-item__actions {
  display: flex;
  gap: 6px;
}

.preset-editor-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.5);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
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

.preset-editor {
  position: relative;
  z-index: 1;
  background: var(--color-bg-surface);
  border-radius: var(--radius-md);
  padding: 24px;
  width: 90%;
  max-width: 560px;
  max-height: 85vh;
  overflow-y: auto;
}

.preset-editor__title {
  font-size: 16px;
  font-weight: 600;
  margin-bottom: 16px;
  color: var(--color-text-primary);
}

.preset-editor__field {
  display: block;
  margin-bottom: 12px;
}

.preset-editor__field span {
  display: block;
  font-size: 12px;
  color: var(--color-text-secondary);
  margin-bottom: 4px;
}

.preset-editor__input,
.preset-editor__textarea {
  width: 100%;
  padding: 8px;
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-sm);
  background: var(--color-bg-input, var(--color-bg-surface-container));
  color: var(--color-text-primary);
  font-family: var(--font-sans);
  font-size: 13px;
}

.preset-editor__textarea {
  font-family: var(--font-mono);
  resize: vertical;
}

.preset-editor__actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 16px;
}
</style>
