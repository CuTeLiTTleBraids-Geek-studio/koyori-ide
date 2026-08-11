<script setup lang="ts">
// Koyori IDE 组件 · Ai Skills View。
// 喵，这是 Ai Skills View，负责 Koyori IDE 的界面呈现喵~
import SkillsSection from "@/components/settings/ai/SkillsSection.vue";
import { useI18n } from "@/lib/i18n";

const { t } = useI18n();
</script>

<template>
  <section class="ai-workspace-page">
    <header class="ai-workspace-page__header">
      <p class="ai-workspace-page__eyebrow">{{ t("aiWorkspace.skills") }}</p>
      <h1>{{ t("settings.skills") }}</h1>
      <p>{{ t("aiWorkspace.skillsDesc") }}</p>
    </header>
    <div class="ai-workspace-page__content"><SkillsSection /></div>
  </section>
</template>

<style scoped>
.ai-workspace-page { height: 100%; overflow: auto; padding: 24px; }
.ai-workspace-page__header { max-width: 760px; margin-bottom: 20px; }
.ai-workspace-page__eyebrow { color: var(--color-primary); font-size: 12px; font-weight: 600; letter-spacing: .08em; text-transform: uppercase; }
.ai-workspace-page__header h1 { margin: 4px 0 6px; font-size: 28px; }
.ai-workspace-page__header > p:last-child { color: var(--color-text-secondary); }
.ai-workspace-page__content { max-width: 1080px; }
</style>
