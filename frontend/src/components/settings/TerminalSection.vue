<script setup lang="ts">
// Koyori IDE 组件 · Terminal Section。
// 喵，这是 Terminal Section，负责 Koyori IDE 的界面呈现喵~
import { appState, saveSettings } from "@/stores/app";
import { useI18n } from "@/lib/i18n";

const { t } = useI18n();
</script>

<template>
  <section class="settings-section">
    <h2 class="section-title">{{ t("terminalSection.title") }}</h2>

    <div class="setting-row">
      <label class="setting-label">{{ t("terminalSection.defaultShell") }}</label>
      <div class="setting-control">
        <el-select
          v-model="appState.defaultShell"
          size="default"
          style="width: var(--setting-control-width-sm)"
          :aria-label="t('terminalSection.defaultShellAria')"
          @change="saveSettings"
        >
          <el-option :label="t('terminalSection.shellPowerShell')" value="powershell" />
          <el-option :label="t('terminalSection.shellCmd')" value="cmd" />
          <el-option :label="t('terminalSection.shellBash')" value="bash" />
          <el-option :label="t('terminalSection.shellWsl')" value="wsl" />
        </el-select>
      </div>
    </div>

    <div class="setting-row">
      <label class="setting-label">{{ t("terminalSection.fontSize") }}</label>
      <div class="setting-control">
        <el-input-number
          v-model="appState.terminalFontSize"
          :min="8"
          :max="32"
          :step="1"
          size="default"
          :aria-label="t('terminalSection.fontSizeAria')"
          @change="saveSettings"
        />
      </div>
    </div>

    <div class="setting-row">
      <label class="setting-label">{{ t("terminalSection.cursorStyle") }}</label>
      <div class="setting-control">
        <el-select
          v-model="appState.terminalCursorStyle"
          size="default"
          style="width: var(--setting-control-width-sm)"
          :aria-label="t('terminalSection.cursorStyleAria')"
          @change="saveSettings"
        >
          <el-option :label="t('terminalSection.cursorStyleBlock')" value="block" />
          <el-option :label="t('terminalSection.cursorStyleUnderline')" value="underline" />
          <el-option :label="t('terminalSection.cursorStyleBar')" value="bar" />
        </el-select>
      </div>
    </div>

    <div class="setting-row">
      <label class="setting-label">{{ t("terminalSection.scrollback") }}</label>
      <div class="setting-control">
        <el-input-number
          v-model="appState.scrollback"
          :min="100"
          :max="100000"
          :step="1000"
          size="default"
          :aria-label="t('terminalSection.scrollbackAria')"
          @change="saveSettings"
        />
      </div>
    </div>
  </section>
</template>
