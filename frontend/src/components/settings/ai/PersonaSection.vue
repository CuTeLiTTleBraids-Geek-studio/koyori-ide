<script setup lang="ts">
// Koyori IDE 组件 · Persona Section。
// 喵，这是 Persona Section，负责 Koyori IDE 的界面呈现喵~
/**
 * Plan 11 Task 8 Step 5 - Persona 设置分区。
 * 展示 Persona 列表（内置 7 个 + 用户自定义）、编辑器与市场导出/导入。
 * 内置 Persona 不可删除，也不可修改 SystemPrompt（仅允许关联知识库）。
 */
import { onMounted, ref } from "vue";
import { ElMessageBox } from "element-plus";
import { useI18n } from "@/lib/i18n";
import {
  personaState,
  personasList,
  loadPersonas,
  createPersona,
  updatePersona,
  deletePersona,
  exportPersona,
  importPersona,
} from "@/stores/persona";
import type { Persona } from "@/stores/persona";
import FocusTrapDialog from "@/components/common/FocusTrapDialog.vue";

const { t } = useI18n();

onMounted(async () => {
  await loadPersonas();
});

const editingPersona = ref<Persona | null>(null);
const showEditor = ref(false);
const importText = ref("");

function openEditor(p?: Persona): void {
  if (p) {
    editingPersona.value = { ...p, expertise: p.expertise ? [...p.expertise] : undefined, knowledgeBase: p.knowledgeBase ? [...p.knowledgeBase] : undefined };
  } else {
    editingPersona.value = {
      id: "",
      name: "",
      systemPrompt: "",
      tone: "",
      expertise: [],
      knowledgeBase: [],
      defaultModel: "",
      defaultMode: "chat",
      builtIn: false,
    };
  }
  showEditor.value = true;
}

async function savePersona(): Promise<void> {
  const p = editingPersona.value;
  if (!p || !p.id || !p.name) return;
  if (p.builtIn) {
    await updatePersona(p);
  } else {
    // 尝试 update，失败则 create
    const ok = await updatePersona(p);
    if (!ok && personaState.error?.includes("not found")) {
      await createPersona(p);
    }
  }
  showEditor.value = false;
  editingPersona.value = null;
}

async function handleDelete(p: Persona): Promise<void> {
  try {
    await ElMessageBox.confirm(
      t("personaSection.deleteConfirm", { name: p.name }),
      t("common.confirm"),
      { type: "warning", confirmButtonText: t("common.confirm"), cancelButtonText: t("common.cancel") },
    );
  } catch {
    return;
  }
  await deletePersona(p.id);
}

async function handleExport(p: Persona): Promise<void> {
  const data = await exportPersona(p.id);
  if (data) {
    // 创建下载链接
    const blob = new Blob([data], { type: "application/json" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `${p.id}.persona.json`;
    a.click();
    URL.revokeObjectURL(url);
  } else if (personaState.error) {
    alert(personaState.error);
  }
}

async function handleImport(): Promise<void> {
  if (!importText.value) return;
  const ok = await importPersona(importText.value);
  if (ok) {
    importText.value = "";
    showEditor.value = false;
  } else if (personaState.error) {
    alert(personaState.error);
  }
}

function onImportFile(e: Event): void {
  const input = e.target as HTMLInputElement;
  const file = input.files?.[0];
  if (!file) return;
  const reader = new FileReader();
  reader.onload = () => {
    importText.value = reader.result as string;
  };
  reader.readAsText(file);
}
</script>

<template>
  <section class="settings-section">
    <h2 class="section-title">{{ t("settings.persona") }}</h2>
    <p class="section-hint">{{ t("personaSection.hint") }}</p>

    <div v-if="personaState.error" class="persona-error">{{ personaState.error }}</div>

    <!-- 工具栏 -->
    <div class="persona-toolbar">
      <el-button size="small" type="primary" @click="openEditor()">{{ t("personaSection.add") }}</el-button>
      <label class="persona-import-btn">
        <span>{{ t("personaSection.import") }}</span>
        <input type="file" accept=".json" @change="onImportFile" hidden />
      </label>
      <textarea
        v-if="importText"
        v-model="importText"
        class="persona-import-text"
        :placeholder="t('personaSection.importPlaceholder')"
        rows="3"
      ></textarea>
      <el-button v-if="importText" size="small" type="success" @click="handleImport">{{ t("personaSection.import") }}</el-button>
    </div>

    <!-- Persona 列表 -->
    <div v-if="personasList.length === 0 && !personaState.loading" class="persona-empty">
      {{ t("personaSection.empty") }}
    </div>

    <div v-else class="persona-list">
      <div v-for="p in personasList" :key="p.id" class="persona-card">
        <div class="persona-card-header">
          <span class="persona-name">{{ p.name }}</span>
          <span v-if="p.builtIn" class="persona-badge persona-badge--builtin">{{ t("personaSection.builtin") }}</span>
          <span v-else class="persona-badge persona-badge--custom">{{ t("personaSection.custom") }}</span>
          <span v-if="p.tone" class="persona-tone">{{ p.tone }}</span>
        </div>
        <p class="persona-prompt-preview">{{ p.systemPrompt.length > 100 ? p.systemPrompt.slice(0, 100) + "..." : p.systemPrompt }}</p>
        <div v-if="p.expertise?.length" class="persona-expertise">
          <code v-for="e in p.expertise" :key="e" class="persona-chip">{{ e }}</code>
        </div>
        <div class="persona-actions">
          <el-button size="small" @click="openEditor(p)">{{ t("common.edit") }}</el-button>
          <el-button size="small" type="success" @click="handleExport(p)">{{ t("personaSection.export") }}</el-button>
          <el-button v-if="!p.builtIn" size="small" type="danger" @click="handleDelete(p)">{{ t("common.delete") }}</el-button>
        </div>
      </div>
    </div>

    <!-- 编辑器对话框 -->
    <div
      v-if="showEditor && editingPersona"
      class="persona-editor-overlay"
    >
      <button
        type="button"
        class="dialog-backdrop-button"
        tabindex="-1"
        :aria-label="t('a11y.closeDialog')"
        @click="showEditor = false"
      />
      <FocusTrapDialog
        class="persona-editor"
        :aria-label="editingPersona.builtIn ? t('personaSection.editBuiltin') : t('personaSection.editPersona')"
        @close="showEditor = false"
      >
        <h3 class="persona-editor__title">{{ editingPersona.builtIn ? t("personaSection.editBuiltin") : t("personaSection.editPersona") }}</h3>
        <div class="persona-editor__field">
          <label class="persona-label">{{ t("personaSection.fieldId") }}</label>
          <input type="text" v-model="editingPersona.id" :disabled="editingPersona.builtIn" class="persona-input" />
        </div>
        <div class="persona-editor__field">
          <label class="persona-label">{{ t("personaSection.fieldName") }}</label>
          <input type="text" v-model="editingPersona.name" :disabled="editingPersona.builtIn" class="persona-input" />
        </div>
        <div class="persona-editor__field">
          <label class="persona-label">{{ t("personaSection.fieldTone") }}</label>
          <input type="text" v-model="editingPersona.tone" class="persona-input" />
        </div>
        <div class="persona-editor__field">
          <label class="persona-label">{{ t("personaSection.fieldExpertise") }}</label>
          <input type="text" :value="editingPersona.expertise?.join(', ')" @input="editingPersona.expertise = ($event.target as HTMLInputElement).value.split(',').map(s=>s.trim()).filter(s=>s)" class="persona-input" />
        </div>
        <div class="persona-editor__field">
          <label class="persona-label">{{ t("personaSection.fieldSystemPrompt") }}</label>
          <textarea
            v-model="editingPersona.systemPrompt"
            :disabled="editingPersona.builtIn"
            class="persona-input persona-input--textarea"
            rows="4"
          ></textarea>
        </div>
        <div class="persona-editor__field">
          <label class="persona-label">{{ t("personaSection.fieldKnowledgeBase") }}</label>
          <input type="text" :value="editingPersona.knowledgeBase?.join(', ')" @input="editingPersona.knowledgeBase = ($event.target as HTMLInputElement).value.split(',').map(s=>s.trim()).filter(s=>s)" class="persona-input" />
        </div>
        <div class="persona-editor__field">
          <label class="persona-label">{{ t("personaSection.fieldDefaultModel") }}</label>
          <input type="text" v-model="editingPersona.defaultModel" class="persona-input" />
        </div>
        <div class="persona-editor__field">
          <label class="persona-label">{{ t("personaSection.fieldDefaultMode") }}</label>
          <select v-model="editingPersona.defaultMode" class="persona-input">
            <option value="chat">Chat</option>
            <option value="plan">Plan</option>
            <option value="goal">Goal</option>
            <option value="agent">Agent</option>
          </select>
        </div>
        <div class="persona-editor__actions">
          <el-button size="small" @click="showEditor = false">{{ t("common.cancel") }}</el-button>
          <el-button size="small" type="primary" :loading="personaState.saving" @click="savePersona">{{ t("common.save") }}</el-button>
        </div>
      </FocusTrapDialog>
    </div>
  </section>
</template>

<style scoped>
.section-hint {
  font-size: 13px;
  color: var(--color-text-secondary);
  margin-bottom: 12px;
  line-height: 1.5;
}

.persona-error {
  color: var(--color-error, #f44336);
  font-size: 12px;
  margin-bottom: 16px;
}

.persona-toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 16px;
  flex-wrap: wrap;
}

.persona-import-btn {
  font-size: 12px;
  padding: 6px 12px;
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-xs);
  cursor: pointer;
  background: var(--color-bg-surface);
}

.persona-import-text {
  font-family: var(--font-mono);
  font-size: 11px;
  padding: 6px;
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-xs);
  width: 100%;
  margin-top: 8px;
  background: var(--color-bg-surface);
}

.persona-empty {
  font-size: 13px;
  color: var(--color-text-tertiary);
  padding: 24px 0;
  text-align: center;
}

.persona-list {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 12px;
}

.persona-card {
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-sm);
  padding: 12px;
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.persona-card-header {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}

.persona-name {
  font-size: 14px;
  font-weight: 600;
  color: var(--color-text-primary);
}

.persona-badge {
  font-size: 10px;
  padding: 1px 5px;
  border-radius: var(--radius-xs);
  text-transform: uppercase;
  font-weight: 500;
}

.persona-badge--builtin {
  color: var(--color-primary, #2196f3);
  background: var(--color-primary-container, rgba(33, 150, 243, 0.1));
}

.persona-badge--custom {
  color: var(--color-success, #4caf50);
  background: var(--color-success-container, rgba(76, 175, 80, 0.1));
}

.persona-tone {
  font-size: 11px;
  color: var(--color-text-tertiary);
  font-style: italic;
}

.persona-prompt-preview {
  font-size: 12px;
  color: var(--color-text-secondary);
  line-height: 1.4;
  margin: 0;
}

.persona-expertise {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}

.persona-chip {
  font-family: var(--font-mono);
  font-size: 10px;
  color: var(--color-text-primary);
  background: var(--color-bg-surface-container);
  padding: 1px 5px;
  border-radius: var(--radius-xs);
}

.persona-actions {
  display: flex;
  gap: 6px;
  margin-top: 4px;
}

.persona-editor-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.4);
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

.persona-editor {
  position: relative;
  z-index: 1;
  background: var(--color-bg-surface);
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-md);
  padding: 24px;
  width: 560px;
  max-width: 90vw;
  max-height: 90vh;
  overflow-y: auto;
}

.persona-editor__title {
  font-size: 16px;
  font-weight: 600;
  margin-bottom: 16px;
  color: var(--color-text-primary);
}

.persona-editor__field {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin-bottom: 14px;
}

.persona-label {
  font-size: 12px;
  color: var(--color-text-secondary);
  font-weight: 500;
}

.persona-input {
  font-size: 13px;
  padding: 6px 8px;
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-xs);
  background: var(--color-bg-surface);
  color: var(--color-text-primary);
  width: 100%;
  box-sizing: border-box;
}

.persona-input:disabled {
  background: var(--color-bg-surface-container-low);
  color: var(--color-text-tertiary);
}

.persona-input--textarea {
  font-family: var(--font-mono);
  resize: vertical;
  min-height: 80px;
}

.persona-editor__actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 16px;
}
</style>
