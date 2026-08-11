<script setup lang="ts">
// Koyori IDE 组件 · Git Section。
// 喵，这是 Git Section，负责 Koyori IDE 的界面呈现喵~
import { appState, saveSettings } from "@/stores/app";
import { useI18n } from "@/lib/i18n";

const { t } = useI18n();
</script>

<template>
  <section class="settings-section">
    <h2 class="section-title">{{ t("activity.sourceControl") }}</h2>

    <div class="setting-row">
      <label class="setting-label">{{ t("editorSection.gitBlame") }}</label>
      <div class="setting-control">
        <el-switch
          v-model="appState.gitBlameEnabled"
          :aria-label="t('editorSection.gitBlameAria')"
          @change="saveSettings"
        />
      </div>
    </div>
  </section>
</template>
