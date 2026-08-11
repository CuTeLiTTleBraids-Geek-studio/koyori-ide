<script setup lang="ts">
// Koyori IDE 组件 · Lsp Section。
// 喵，这是 Lsp Section，负责 Koyori IDE 的界面呈现喵~
import { appState, saveSettings } from "@/stores/app";
import { useI18n } from "@/lib/i18n";

const { t } = useI18n();
</script>

<template>
  <section class="settings-section">
    <h2 class="section-title">{{ t("settings.lsp") }}</h2>

    <div class="setting-row">
      <label class="setting-label">{{ t("editorSection.organizeImportsOnSave") }}</label>
      <div class="setting-control">
        <el-switch
          v-model="appState.organizeImportsOnSave"
          :aria-label="t('editorSection.organizeImportsOnSaveAria')"
          @change="saveSettings"
        />
      </div>
    </div>

    <div class="setting-row">
      <label class="setting-label">{{ t("editorSection.inlayHints") }}</label>
      <div class="setting-control">
        <el-switch
          v-model="appState.inlayHintsEnabled"
          :aria-label="t('editorSection.inlayHintsAria')"
          @change="saveSettings"
        />
      </div>
    </div>
  </section>
</template>
