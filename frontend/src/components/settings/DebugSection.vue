<script setup lang="ts">
// Koyori IDE 组件 · Debug Section。
// 喵，这是 Debug Section，负责 Koyori IDE 的界面呈现喵~
import { computed, watch } from "vue";
import { appState } from "@/stores/app";
import {
  debugState,
  loadLaunchConfigs,
  upsertLaunchConfig,
} from "@/stores/debug";
import { useI18n } from "@/lib/i18n";

const { t } = useI18n();

const activeConfigName = computed({
  get: () => debugState.activeConfigName || debugState.launchConfigs[0]?.name || "",
  set: (value: string) => { debugState.activeConfigName = value; },
});

const activeConfig = computed(() =>
  debugState.launchConfigs.find((config) => config.name === activeConfigName.value) ?? null,
);

const stopOnEntry = computed({
  get: () => Boolean(activeConfig.value?.stopOnEntry),
  set: (value: boolean) => {
    if (!activeConfig.value) return;
    upsertLaunchConfig({ ...activeConfig.value, stopOnEntry: value });
  },
});

watch(
  () => appState.currentProject,
  (projectRoot) => { void loadLaunchConfigs(projectRoot ?? undefined); },
  { immediate: true },
);
</script>

<template>
  <section class="settings-section">
    <h2 class="section-title">{{ t("view.debug.title") }}</h2>

    <div class="setting-row">
      <label class="setting-label">{{ t("debugSection.launchConfiguration") }}</label>
      <div class="setting-control">
        <el-select
          v-model="activeConfigName"
          :disabled="debugState.launchConfigs.length === 0"
          :aria-label="t('debugSection.launchConfigurationAria')"
          style="width: var(--setting-control-width, 280px)"
        >
          <el-option
            v-for="config in debugState.launchConfigs"
            :key="config.name"
            :label="config.name"
            :value="config.name"
          />
        </el-select>
      </div>
    </div>

    <div class="setting-row">
      <label class="setting-label">{{ t("debugSection.stopOnEntry") }}</label>
      <div class="setting-control">
        <el-switch
          v-model="stopOnEntry"
          :disabled="!activeConfig"
          :aria-label="t('debugSection.stopOnEntryAria')"
        />
      </div>
    </div>
  </section>
</template>
