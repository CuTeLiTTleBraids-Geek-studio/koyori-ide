<script setup lang="ts">
// Koyori IDE 组件 · Agent Section。
// 喵，这是 Agent Section，负责 Koyori IDE 的界面呈现喵~
import { computed } from "vue";
import { appState, saveSettings } from "@/stores/app";
import type { AgentPermissionMode } from "@/types";
import { useI18n } from "@/lib/i18n";

const { t } = useI18n();





const permissionOptions = computed<Array<{
	value: AgentPermissionMode;
	label: string;
}>>(() => [
	{ value: "always-ask", label: t("agentSection.permissionAlwaysAsk") },
	{ value: "assist", label: t("agentSection.permissionAssist") },
	{ value: "allow-all", label: t("agentSection.permissionAllowAll") },
]);

const activePermissionDescription = computed(() => {
	switch (appState.agentPermissionMode) {
		case "assist": return t("agentSection.permissionAssistDescription");
		case "allow-all": return t("agentSection.permissionAllowAllDescription");
		default: return t("agentSection.permissionAlwaysAskDescription");
	}
});

function setPermissionMode(mode: AgentPermissionMode): void {
	appState.agentPermissionMode = mode;
	saveSettings();
}
</script>

<template>
  <section class="settings-section">
    <h2 class="section-title">{{ t("settings.agent") }}</h2>
    <p class="section-hint">{{ t("agentSection.hint") }}</p>

    <div class="permission-setting">
      <span class="permission-setting__label">{{ t("agentSection.permissionMode") }}</span>
      <el-radio-group
        :model-value="appState.agentPermissionMode"
        size="small"
        :aria-label="t('agentSection.permissionMode')"
        @change="(value: AgentPermissionMode) => setPermissionMode(value)"
      >
        <el-radio-button
          v-for="option in permissionOptions"
          :key="option.value"
          :value="option.value"
        >
          {{ option.label }}
        </el-radio-button>
      </el-radio-group>
      <p class="permission-setting__description">{{ activePermissionDescription }}</p>
    </div>

    <p class="section-warning">
      <strong>{{ t("agentSection.warningLabel") }}</strong>
      {{ t("agentSection.warning") }}
    </p>

  </section>
</template>

<style scoped>
.section-hint {
  font-size: 13px;
  color: var(--color-text-secondary);
  margin-bottom: 16px;
  line-height: 1.5;
}

.permission-setting {
  margin-bottom: 16px;
}

.permission-setting__label {
  display: block;
  margin-bottom: 8px;
  color: var(--color-text-primary);
  font-size: 12px;
  font-weight: 600;
}

.permission-setting__description {
  margin: 8px 0 0;
  color: var(--color-text-tertiary);
  font-size: 12px;
  line-height: 1.5;
}

.section-warning {
  font-size: 12px;
  color: var(--color-text-tertiary);
  margin-bottom: 20px;
  padding: 8px 12px;
  background: var(--color-bg-surface-container-low);
  border-radius: var(--radius-sm);
  border-left: 3px solid var(--color-warning, #ff9800);
  line-height: 1.5;
}


</style>
