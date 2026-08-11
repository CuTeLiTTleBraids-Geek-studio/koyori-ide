<script setup lang="ts">
// Koyori IDE 组件 · Ai Automation View。
// 喵，这是 Ai Automation View，负责 Koyori IDE 的界面呈现喵~
import { ref } from "vue";
import PlanSection from "@/components/settings/ai/PlanSection.vue";
import GoalSection from "@/components/settings/ai/GoalSection.vue";
import WorkflowSection from "@/components/settings/ai/WorkflowSection.vue";
import { useI18n } from "@/lib/i18n";

type AutomationTab = "plans" | "goals" | "workflows";

const { t } = useI18n();
const activeTab = ref<AutomationTab>("plans");

const tabs: Array<{ key: AutomationTab; labelKey: string; descriptionKey: string }> = [
  { key: "plans", labelKey: "settings.plan", descriptionKey: "aiWorkspace.automationPlansDesc" },
  { key: "goals", labelKey: "settings.goal", descriptionKey: "aiWorkspace.automationGoalsDesc" },
  { key: "workflows", labelKey: "settings.workflow", descriptionKey: "aiWorkspace.automationWorkflowsDesc" },
];
</script>

<template>
  <section class="ai-automation-view">
    <header class="ai-workspace-page__header">
      <p class="ai-workspace-page__eyebrow">{{ t("aiWorkspace.automation") }}</p>
      <h1>{{ t("aiWorkspace.automation") }}</h1>
      <p>{{ t("aiWorkspace.automationDesc") }}</p>
    </header>

    <div class="ai-automation-view__tabs" role="tablist" :aria-label="t('aiWorkspace.automation')">
      <button
        v-for="tab in tabs"
        :key="tab.key"
        type="button"
        role="tab"
        class="ai-automation-view__tab"
        :class="{ 'is-active': activeTab === tab.key }"
        :data-tab="tab.key"
        :aria-selected="activeTab === tab.key"
        @click="activeTab = tab.key"
      >
        <strong>{{ t(tab.labelKey) }}</strong>
        <small>{{ t(tab.descriptionKey) }}</small>
      </button>
    </div>

    <div class="ai-automation-view__content">
      <PlanSection v-if="activeTab === 'plans'" />
      <GoalSection v-else-if="activeTab === 'goals'" />
      <WorkflowSection v-else />
    </div>
  </section>
</template>

<style scoped>
.ai-automation-view { height: 100%; overflow: auto; padding: 24px; }
.ai-workspace-page__header { max-width: 760px; margin-bottom: 20px; }
.ai-workspace-page__eyebrow { color: var(--color-primary); font-size: 12px; font-weight: 600; letter-spacing: .08em; text-transform: uppercase; }
.ai-workspace-page__header h1 { margin: 4px 0 6px; font-size: 28px; }
.ai-workspace-page__header > p:last-child { color: var(--color-text-secondary); }
.ai-automation-view__tabs { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 10px; max-width: 920px; margin-bottom: 18px; }
.ai-automation-view__tab { display: grid; gap: 3px; padding: 12px 14px; border: 1px solid var(--color-border-default); border-radius: var(--radius-sm); color: var(--color-text-secondary); background: var(--color-bg-surface-container-low); text-align: left; cursor: pointer; transition: border-color var(--transition-fast), background-color var(--transition-fast), transform var(--transition-fast); }
.ai-automation-view__tab:hover { border-color: var(--color-border-strong); }
.ai-automation-view__tab:active { transform: scale(.98); }
.ai-automation-view__tab.is-active { color: var(--color-text-primary); border-color: var(--color-primary); background: var(--color-bg-surface); }
.ai-automation-view__tab small { color: var(--color-text-tertiary); font-size: 11px; }
.ai-automation-view__content { max-width: 1120px; }
@media (max-width: 760px) { .ai-automation-view__tabs { grid-template-columns: 1fr; } }
@media (prefers-reduced-motion: reduce) { .ai-automation-view__tab { transition: none; } .ai-automation-view__tab:active { transform: none; } }
</style>
