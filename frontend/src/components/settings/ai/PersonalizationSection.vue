<script setup lang="ts">
// Koyori IDE 组件 · Personalization Section；交互服务：设置（SettingsService）。
// 喵，这是 Personalization Section，负责 Koyori IDE 的界面呈现喵~
/**
 * Plan 11 Task 15 — PersonalizationSection.vue
 *
 * Step 3: 上传 + 预览 + 透明度/模糊滑块 + 字体 + 气泡样式 + 头像
 * Step 1: PersonalizationConfig schema（镜像后端）
 * Step 2: 图片存储调用 settingsService.savePersonalizationAsset
 *
 * 安全：文件上传经后端 basename 清洗 + 路径校验（G-SEC-06），不直接操作文件系统。
 */
import { ref } from "vue";
import { useI18n } from "@/lib/i18n";
import { appState, saveSettings } from "@/stores/app";
import { settingsService } from "@/api/services";
import { notifyError, notifySuccess } from "@/lib/notifications";
import {
  applyPersonalization,
  invalidatePersonalizationAsset,
} from "@/composables/usePersonalization";
import type { PersonalizationConfig } from "@/types";

const { t } = useI18n();

const p = appState.personalization as PersonalizationConfig;

const bubbleStyles: Array<{ value: PersonalizationConfig["bubbleStyle"]; labelKey: string }> = [
  { value: "rounded", labelKey: "personalization.bubbleRounded" },
  { value: "sharp", labelKey: "personalization.bubbleSharp" },
  { value: "bubble", labelKey: "personalization.bubbleBubble" },
];

// ---- Step 2: 图片上传 ----

async function handleUploadImage(field: "codeEditorBgImage" | "chatBgImage" | "userAvatar" | "aiAvatar", file: File): Promise<void> {
  if (file.size > 8 * 1024 * 1024) {
    notifyError(t("personalization.imageTooLarge"));
    return;
  }
  try {
    const data = new Uint8Array(await file.arrayBuffer());
    const relPath = await settingsService.savePersonalizationAsset(file.name, data);
    const previousPath = p[field];
    invalidatePersonalizationAsset(relPath);
    p[field] = relPath;
    if (previousPath === relPath) applyPersonalization();
    saveSettings();
    notifySuccess(t("personalization.imageUploaded"));
  } catch (e: unknown) {
    notifyError(e instanceof Error ? e.message : String(e));
  }
}

function onFileInput(field: "codeEditorBgImage" | "chatBgImage" | "userAvatar" | "aiAvatar", e: Event): void {
  const input = e.target as HTMLInputElement;
  if (input.files && input.files[0]) {
    void handleUploadImage(field, input.files[0]);
  }
}

// ---- Step 9: 导入/导出 .koyori-ide-theme.json ----

function handleExportTheme(): void {
  const theme = { personalization: { ...p } };
  const blob = new Blob([JSON.stringify(theme, null, 2)], { type: "application/json" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = "theme.koyori-ide-theme.json";
  a.click();
  URL.revokeObjectURL(url);
}

const importInput = ref<HTMLInputElement | null>(null);

function handleImportTheme(e: Event): void {
  const input = e.target as HTMLInputElement;
  if (!input.files || !input.files[0]) return;
  const file = input.files[0];
  const reader = new FileReader();
  reader.onload = () => {
    try {
      const parsed = JSON.parse(reader.result as string);
      if (parsed.personalization) {
        Object.assign(p, parsed.personalization);
        saveSettings();
        notifySuccess(t("personalization.themeImported"));
      }
    } catch {
      notifyError(t("personalization.invalidThemeFile"));
    }
  };
  reader.readAsText(file);
}

function onSliderChange(): void {
  saveSettings();
}
</script>

<template>
  <div class="personalization-section">
    <h3 class="personalization-section__title">{{ t("personalization.title") }}</h3>
    <p class="personalization-section__desc">{{ t("personalization.description") }}</p>

    <!-- Step 3: 代码编辑器背景 -->
    <div class="personalization-section__group">
      <h4>{{ t("personalization.codeEditorBg") }}</h4>
      <label class="personalization-section__upload">
        <input type="file" accept="image/*" @change="onFileInput('codeEditorBgImage', $event)" />
        <span>{{ t("personalization.uploadImage") }}</span>
      </label>
      <p v-if="p.codeEditorBgImage" class="personalization-section__path">{{ p.codeEditorBgImage }}</p>
      <div class="personalization-section__slider">
        <label>{{ t("personalization.opacity") }}: {{ Math.round((p.codeEditorBgOpacity ?? 0) * 100) }}%</label>
        <input type="range" min="0" max="1" step="0.05" v-model.number="p.codeEditorBgOpacity" @change="onSliderChange" />
      </div>
      <div class="personalization-section__slider">
        <label>{{ t("personalization.blur") }}: {{ p.codeEditorBgBlur ?? 0 }}px</label>
        <input type="range" min="0" max="20" step="1" v-model.number="p.codeEditorBgBlur" @change="onSliderChange" />
      </div>
    </div>

    <!-- Step 3: 对话框背景 -->
    <div class="personalization-section__group">
      <h4>{{ t("personalization.chatBg") }}</h4>
      <label class="personalization-section__upload">
        <input type="file" accept="image/*" @change="onFileInput('chatBgImage', $event)" />
        <span>{{ t("personalization.uploadImage") }}</span>
      </label>
      <p v-if="p.chatBgImage" class="personalization-section__path">{{ p.chatBgImage }}</p>
      <div class="personalization-section__slider">
        <label>{{ t("personalization.opacity") }}: {{ Math.round((p.chatBgOpacity ?? 0) * 100) }}%</label>
        <input type="range" min="0" max="1" step="0.05" v-model.number="p.chatBgOpacity" @change="onSliderChange" />
      </div>
      <div class="personalization-section__slider">
        <label>{{ t("personalization.blur") }}: {{ p.chatBgBlur ?? 0 }}px</label>
        <input type="range" min="0" max="20" step="1" v-model.number="p.chatBgBlur" @change="onSliderChange" />
      </div>
    </div>

    <!-- Step 3: 头像 -->
    <div class="personalization-section__group">
      <h4>{{ t("personalization.avatars") }}</h4>
      <div class="personalization-section__avatar-row">
        <label class="personalization-section__upload">
          <input type="file" accept="image/*" @change="onFileInput('userAvatar', $event)" />
          <span>{{ t("personalization.userAvatar") }}</span>
        </label>
        <span v-if="p.userAvatar" class="personalization-section__path">{{ p.userAvatar }}</span>
      </div>
      <div class="personalization-section__avatar-row">
        <label class="personalization-section__upload">
          <input type="file" accept="image/*" @change="onFileInput('aiAvatar', $event)" />
          <span>{{ t("personalization.aiAvatar") }}</span>
        </label>
        <span v-if="p.aiAvatar" class="personalization-section__path">{{ p.aiAvatar }}</span>
      </div>
    </div>

    <!-- Step 3: 字体 -->
    <div class="personalization-section__group">
      <h4>{{ t("personalization.font") }}</h4>
      <label class="personalization-section__label">
        <span>{{ t("personalization.fontFamily") }}</span>
        <input v-model="p.fontFamily" class="personalization-section__input" @change="onSliderChange" />
      </label>
      <label class="personalization-section__label">
        <span>{{ t("personalization.fontSize") }}</span>
        <input type="number" min="10" max="24" v-model.number="p.fontSize" class="personalization-section__input" @change="onSliderChange" />
      </label>
    </div>

    <!-- Step 7: 气泡样式 -->
    <div class="personalization-section__group">
      <h4>{{ t("personalization.bubbleStyle") }}</h4>
      <div class="personalization-section__bubble-options">
        <button
          v-for="bs in bubbleStyles"
          :key="bs.value"
          :class="{ 'is-active': p.bubbleStyle === bs.value }"
          @click="p.bubbleStyle = bs.value; onSliderChange()"
        >
          {{ t(bs.labelKey) }}
        </button>
      </div>
      <div class="personalization-section__slider">
        <label>{{ t("personalization.bubbleOpacity") }}: {{ Math.round((p.bubbleOpacity ?? 1) * 100) }}%</label>
        <input type="range" min="0.3" max="1" step="0.05" v-model.number="p.bubbleOpacity" @change="onSliderChange" />
      </div>
      <div class="personalization-section__slider">
        <label>{{ t("personalization.messageSpacing") }}: {{ p.messageSpacing ?? 12 }}px</label>
        <input type="range" min="0" max="32" step="2" v-model.number="p.messageSpacing" @change="onSliderChange" />
      </div>
    </div>

    <!-- Step 9: 导入/导出 -->
    <div class="personalization-section__group">
      <h4>{{ t("personalization.importExport") }}</h4>
      <div class="personalization-section__io-btns">
        <button class="personalization-section__btn" @click="handleExportTheme">
          {{ t("personalization.exportTheme") }}
        </button>
        <button class="personalization-section__btn" @click="importInput?.click()">
          {{ t("personalization.importTheme") }}
        </button>
        <input
          ref="importInput"
          type="file"
          accept=".json,.koyori-ide-theme.json"
          class="personalization-section__hidden"
          @change="handleImportTheme"
        />
      </div>
    </div>
  </div>
</template>

<style scoped>
.personalization-section {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.personalization-section__title {
  margin: 0;
  font-size: 16px;
}
.personalization-section__desc {
  margin: 0;
  color: var(--el-text-color-secondary, #909399);
  font-size: 13px;
}
.personalization-section__group {
  border: 1px solid var(--el-border-color, #dcdfe6);
  border-radius: 4px;
  padding: 12px;
}
.personalization-section__group h4 {
  margin: 0 0 8px 0;
  font-size: 14px;
}
.personalization-section__upload {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  cursor: pointer;
  font-size: 12px;
  color: var(--el-color-primary, #409eff);
}
.personalization-section__upload input[type="file"] {
  font-size: 11px;
}
.personalization-section__path {
  margin: 4px 0;
  font-family: var(--koyori-ide-font-mono, monospace);
  font-size: 11px;
  color: var(--el-text-color-secondary, #909399);
}
.personalization-section__slider {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin-top: 8px;
  font-size: 12px;
}
.personalization-section__slider input[type="range"] {
  width: 100%;
}
.personalization-section__avatar-row {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 6px;
}
.personalization-section__label {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 12px;
  margin-bottom: 8px;
}
.personalization-section__input {
  padding: 6px 8px;
  border: 1px solid var(--el-border-color, #dcdfe6);
  border-radius: 3px;
  font-size: 12px;
  background: var(--el-bg-color, #fff);
  color: var(--el-text-color-primary, #303030);
}
.personalization-section__bubble-options {
  display: flex;
  gap: 6px;
  margin-bottom: 8px;
}
.personalization-section__bubble-options button {
  padding: 6px 12px;
  border: 1px solid var(--el-border-color, #dcdfe6);
  background: transparent;
  color: var(--el-text-color-regular, #606266);
  border-radius: 3px;
  cursor: pointer;
  font-size: 12px;
}
.personalization-section__bubble-options button.is-active {
  border-color: var(--el-color-primary, #409eff);
  background: var(--el-color-primary, #409eff);
  color: #fff;
}
.personalization-section__io-btns {
  display: flex;
  gap: 8px;
}
.personalization-section__btn {
  padding: 6px 16px;
  border: 1px solid var(--el-color-primary, #409eff);
  background: var(--el-color-primary, #409eff);
  color: #fff;
  border-radius: 3px;
  cursor: pointer;
  font-size: 12px;
}
.personalization-section__hidden {
  display: none;
}
</style>
