<script setup lang="ts">
// Koyori IDE 组件 · Profile Section。
// 喵，这是 Profile Section，负责 Koyori IDE 的界面呈现喵~
import { ref, onMounted } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import {
  profiles,
  activeProfileName,
  isLoadingProfiles,
  loadProfiles,
  switchProfile,
  createProfile,
  deleteProfile,
  renameProfile,
  exportProfile,
  importProfile,
} from "@/stores/profiles";
import { errorMessage } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import type { ProfileExport } from "@/types";

const { t } = useI18n();

// New profile dialog state.
const showCreateDialog = ref(false);
const newProfileName = ref("");
const newProfileFromCurrent = ref(true);

// Rename dialog state.
const renameTarget = ref<string | null>(null);
const renameValue = ref("");

// Import dialog state.
const importError = ref<string | null>(null);

onMounted(() => {
  loadProfiles();
});

async function handleSwitch(name: string): Promise<void> {
  if (name === activeProfileName.value) return;
  try {
    await switchProfile(name);
    ElMessage.success(t("profile.switchedTo", { name }));
  } catch (e: unknown) {
    ElMessage.error(errorMessage(e));
  }
}

async function handleCreate(): Promise<void> {
  const name = newProfileName.value.trim();
  if (!name) {
    ElMessage.warning(t("profile.nameRequired"));
    return;
  }
  try {
    await createProfile(name, newProfileFromCurrent.value);
    ElMessage.success(t("profile.created", { name }));
    showCreateDialog.value = false;
    newProfileName.value = "";
    newProfileFromCurrent.value = true;
  } catch (e: unknown) {
    ElMessage.error(errorMessage(e));
  }
}

async function handleDelete(name: string): Promise<void> {
  try {
    await ElMessageBox.confirm(
      t("profile.deleteConfirm", { name }),
      t("profile.deleteTitle"),
      { type: "warning", confirmButtonText: t("common.delete"), cancelButtonText: t("common.cancel") },
    );
    await deleteProfile(name);
    ElMessage.success(t("profile.deleted", { name }));
  } catch (e: unknown) {
    if (e === "cancel") return;
    ElMessage.error(errorMessage(e));
  }
}

function startRename(name: string): void {
  renameTarget.value = name;
  renameValue.value = name;
}

async function handleRename(): Promise<void> {
  if (!renameTarget.value) return;
  const newName = renameValue.value.trim();
  if (!newName || newName === renameTarget.value) {
    renameTarget.value = null;
    return;
  }
  try {
    await renameProfile(renameTarget.value, newName);
    ElMessage.success(t("profile.renamedTo", { name: newName }));
  } catch (e: unknown) {
    ElMessage.error(errorMessage(e));
  } finally {
    renameTarget.value = null;
  }
}

async function handleExport(name: string): Promise<void> {
  try {
    const data = await exportProfile(name);
    const blob = new Blob([JSON.stringify(data, null, 2)], { type: "application/json" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `koyoriIde-profile-${name}.json`;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
    ElMessage.success(t("profile.exported", { name }));
  } catch (e: unknown) {
    ElMessage.error(errorMessage(e));
  }
}

async function handleImportFile(file: File): Promise<void> {
  importError.value = null;
  try {
    const text = await file.text();
    const data = JSON.parse(text) as ProfileExport;
    // G25: the schema version is part of the import contract. The backend
    // still rejects unknown versions fail-closed; this frontend check gives
    // an immediate actionable error for files that predate the versioned
    // export format.
    if (typeof data?.schemaVersion !== "number" || data.schemaVersion < 1) {
      throw new Error(t("profile.invalidFile"));
    }
    if (!data.name || !data.settings) {
      throw new Error(t("profile.invalidFile"));
    }
    const usedName = await importProfile(data);
    ElMessage.success(t("profile.importedAs", { name: usedName }));
  } catch (e: unknown) {
    importError.value = errorMessage(e);
    ElMessage.error(errorMessage(e));
  }
}

function onImportInput(e: Event): void {
  const input = e.target as HTMLInputElement;
  if (input.files && input.files.length > 0) {
    handleImportFile(input.files[0]);
    input.value = ""; // reset so same file can be re-imported
  }
}

function formatDate(ts: number | undefined): string {
  if (!ts) return "";
  return new Date(ts * 1000).toLocaleDateString();
}
</script>

<template>
  <section class="settings-section">
    <h2 class="section-title">{{ t("settings.profiles") }}</h2>
    <p class="section-hint">
      {{ t("profile.hint") }}
    </p>

    <div v-if="isLoadingProfiles" class="profile-loading">{{ t("profile.loading") }}</div>

    <div v-else class="profile-list">
      <div
        v-for="p in profiles"
        :key="p.name"
        class="profile-card"
        :class="{ 'is-active': p.active }"
      >
        <div class="profile-card-header">
          <span class="profile-name">{{ p.name }}</span>
          <el-tag v-if="p.active" type="success" size="small">{{ t("profile.active") }}</el-tag>
          <el-tag v-if="p.name === 'default'" size="small">{{ t("profile.default") }}</el-tag>
        </div>
        <div v-if="p.description" class="profile-desc">{{ p.description }}</div>
        <div class="profile-meta">
          <span v-if="p.modifiedAt" class="profile-date">{{ t("profile.modified") }} {{ formatDate(p.modifiedAt) }}</span>
        </div>

        <div v-if="renameTarget === p.name" class="profile-rename-row">
          <el-input
            v-model="renameValue"
            size="small"
            :placeholder="t('profile.newNamePlaceholder')"
            @keyup.enter="handleRename"
            @keyup.escape="renameTarget = null"
          />
          <el-button size="small" type="primary" @click="handleRename">{{ t("common.ok") }}</el-button>
          <el-button size="small" @click="renameTarget = null">{{ t("common.cancel") }}</el-button>
        </div>

        <div v-else class="profile-actions">
          <el-button
            v-if="!p.active"
            size="small"
            type="primary"
            @click="handleSwitch(p.name)"
          >
            {{ t("profile.switch") }}
          </el-button>
          <el-button size="small" @click="startRename(p.name)" :disabled="p.name === 'default'">
            {{ t("profile.rename") }}
          </el-button>
          <el-button size="small" @click="handleExport(p.name)">
            {{ t("profile.export") }}
          </el-button>
          <el-button
            size="small"
            type="danger"
            @click="handleDelete(p.name)"
            :disabled="p.name === 'default' || p.active"
          >
            {{ t("common.delete") }}
          </el-button>
        </div>
      </div>
    </div>

    <div class="profile-toolbar">
      <el-button type="primary" @click="showCreateDialog = true">
        {{ t("profile.newProfile") }}
      </el-button>
      <label class="import-button">
        <el-button>{{ t("profile.importProfile") }}</el-button>
        <input
          type="file"
          accept="application/json,.json"
          class="import-input"
          @change="onImportInput"
        />
      </label>
    </div>

    <!-- Create Profile Dialog -->
    <el-dialog
      v-model="showCreateDialog"
      :title="t('profile.newProfile')"
      width="400px"
      @close="newProfileName = ''"
    >
      <div class="create-form">
        <label class="create-label">{{ t("profile.profileNameLabel") }}</label>
        <el-input
          v-model="newProfileName"
          :placeholder="t('profile.profileNamePlaceholder')"
          @keyup.enter="handleCreate"
        />
        <div class="create-option">
          <el-checkbox v-model="newProfileFromCurrent">
            {{ t("profile.cloneCurrent") }}
          </el-checkbox>
        </div>
      </div>
      <template #footer>
        <el-button @click="showCreateDialog = false">{{ t("common.cancel") }}</el-button>
        <el-button type="primary" @click="handleCreate">{{ t("profile.create") }}</el-button>
      </template>
    </el-dialog>
  </section>
</template>

<style scoped>
.section-hint {
  font-size: 13px;
  color: var(--color-text-secondary);
  margin-bottom: 20px;
  line-height: 1.5;
}

.profile-loading {
  color: var(--color-text-tertiary);
  font-size: 13px;
  padding: 16px 0;
}

.profile-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
  margin-bottom: 20px;
}

.profile-card {
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-md);
  padding: 12px 16px;
  background: var(--color-bg-surface);
  transition: border-color var(--transition-fast);
}

.profile-card.is-active {
  border-color: var(--color-primary);
  background: var(--color-primary-container);
}

.profile-card-header {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 4px;
}

.profile-name {
  font-size: 14px;
  font-weight: 600;
  color: var(--color-text-primary);
}

.profile-desc {
  font-size: 12px;
  color: var(--color-text-secondary);
  margin-bottom: 4px;
}

.profile-meta {
  font-size: 11px;
  color: var(--color-text-tertiary);
  margin-bottom: 8px;
}

.profile-actions {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}

.profile-rename-row {
  display: flex;
  gap: 6px;
  align-items: center;
}

.profile-rename-row .el-input {
  flex: 1;
}

.profile-toolbar {
  display: flex;
  gap: 8px;
  align-items: center;
}

.import-button {
  position: relative;
  display: inline-block;
}

.import-input {
  position: absolute;
  width: 0;
  height: 0;
  opacity: 0;
  overflow: hidden;
}

.create-form {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.create-label {
  font-size: 13px;
  color: var(--color-text-secondary);
}

.create-option {
  margin-top: 4px;
}
</style>
