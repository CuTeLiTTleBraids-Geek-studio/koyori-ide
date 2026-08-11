<script setup lang="ts">
// Koyori IDE 组件 · Assistant Header。
// 喵，这是 Assistant Header，负责 Koyori IDE 的界面呈现喵~
// Plan 11 Task 1 — AI 助手独立页面顶部 Header。
// 模式切换（Chat/Plan/Goal/Agent）、当前模型显示、返回编辑器按钮。
// Persona 选择器在 Task 8 接入；模型 dropdown 在 Task 12 接入完整路由。
import { useI18n } from "@/lib/i18n";
import { aiAssistantState, switchMode } from "@/stores/aiAssistant";
import type { AiMode } from "@/stores/aiAssistant";
import { appState } from "@/stores/app";
import { useRouter } from "vue-router";

const { t } = useI18n();
const router = useRouter();

const modes: AiMode[] = ["chat", "plan", "goal", "agent"];

function handleBack(): void {
  void router.push("/editor");
}
</script>

<template>
  <header class="ai-header">
    <div class="ai-header__left">
      <button class="ai-header__back" @click="handleBack">
        {{ t("aiAssistant.backToEditor") }}
      </button>
    </div>
    <div class="ai-header__modes">
      <button
        v-for="m in modes"
        :key="m"
        class="ai-header__mode"
        :class="{ 'ai-header__mode--active': aiAssistantState.mode === m }"
        @click="switchMode(m)"
      >
        {{ t(`aiAssistant.mode.${m}`) }}
      </button>
    </div>
    <div class="ai-header__right">
      <span class="ai-header__model">
        {{ appState.aiModel || t("aiAssistant.noModel") }}
      </span>
    </div>
  </header>
</template>

<style scoped>
.ai-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 16px;
  border-bottom: 1px solid var(--color-border-subtle, #2a2a2a);
  background: var(--color-bg-surface, #1e1e1e);
  flex-shrink: 0;
}
.ai-header__modes {
  display: flex;
  gap: 4px;
}
.ai-header__mode {
  padding: 4px 12px;
  font-size: 12px;
  border: 1px solid transparent;
  border-radius: 6px;
  background: transparent;
  color: var(--color-text-secondary, #aaa);
  cursor: pointer;
}
.ai-header__mode--active {
  background: var(--color-accent, #3b82f6);
  color: #fff;
}
.ai-header__back {
  padding: 4px 10px;
  font-size: 12px;
  border: 1px solid var(--color-border-default, #3a3a3a);
  border-radius: 6px;
  background: transparent;
  color: var(--color-text-secondary, #aaa);
  cursor: pointer;
}
.ai-header__model {
  font-size: 12px;
  color: var(--color-text-secondary, #aaa);
}
</style>
