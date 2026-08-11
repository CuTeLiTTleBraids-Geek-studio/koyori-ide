<script setup lang="ts">
// Koyori IDE 组件 · Profile View。
// 喵，这是 Profile View，负责 Koyori IDE 的界面呈现喵~
/**
 * F-10 (task-5.md): 独立性能分析视图 (/profile).
 *
 * 将 ProfilePanel.vue 从底部面板升级为完整全屏视图，提供：
 *   - 顶部工具栏（Start/Stop CPU、Heap、Goroutine 抓取）
 *   - 路径输入区（CPU/Heap/Goroutine 自定义输出路径）
 *   - 手动分析区（分析任意 .prof 文件）
 *   - Top functions 表格（flat/cum 百分比）
 *
 * 复用 stores/pprof 的全部状态与 action；ProfilePanel 内部已实现这些能力，
 * 这里以全屏布局重新组织，并补充视图级标题/状态条。
 *
 * i18n 键前缀：view.profile.*
 */
import { onMounted } from "vue";
import ProfilePanel from "@/components/layout/ProfilePanel.vue";
import {
  pprofState,
  refreshProfilingStatus,
  type ProfileKind,
} from "@/stores/pprof";
import { useI18n } from "@/lib/i18n";

const { t } = useI18n();

onMounted(() => {
  void refreshProfilingStatus();
});

// 把 ProfileKind 转为人类可读标签（用于视图级状态条）。
function kindLabel(kind: ProfileKind | ""): string {
  switch (kind) {
    case "cpu":
      return t("view.profile.kindCpu");
    case "heap":
      return t("view.profile.kindHeap");
    case "goroutine":
      return t("view.profile.kindGoroutine");
    case "block":
      return t("view.profile.kindBlock");
    case "mutex":
      return t("view.profile.kindMutex");
    case "trace":
      return t("view.profile.kindTrace");
    default:
      return "—";
  }
}
</script>

<template>
  <div class="profile-view">
    <header class="profile-view__header">
      <h1 class="profile-view__title">{{ t("view.profile.title") }}</h1>
      <p class="profile-view__subtitle">{{ t("view.profile.subtitle") }}</p>
    </header>

    <!--
      视图级状态条：摘要展示当前采样状态 + 最近一次 profile 类型，便于
      用户从顶部一眼看到进度。ProfilePanel 内部仍有更详细的状态展示。
    -->
    <div class="profile-view__statusbar">
      <span v-if="pprofState.activeProfile" class="profile-view__badge profile-view__badge--recording">
        {{ pprofState.activeProfile === "cpu" ? t("view.profile.cpuProfiling") : kindLabel(pprofState.activeProfile) }}
      </span>
      <span v-else class="profile-view__badge profile-view__badge--idle">
        {{ t("view.profile.idle") }}
      </span>
      <span class="profile-view__kind">
        · {{ t("view.profile.lastKind") }}: <strong>{{ kindLabel(pprofState.lastKind) }}</strong>
      </span>
      <span v-if="pprofState.loading" class="profile-view__loading">
        · {{ t("view.profile.loading") }}
      </span>
    </div>

    <!--
      ProfilePanel 已包含工具栏、路径输入区、手动分析区、Top functions 表格
      等全部 pprof 能力。全屏视图直接复用，避免重复实现。
    -->
    <div class="profile-view__body">
      <ProfilePanel />
    </div>
  </div>
</template>

<style scoped>
.profile-view {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  background: var(--color-bg-base);
  color: var(--color-text-primary);
}

.profile-view__header {
  flex-shrink: 0;
  padding: 12px 16px;
  border-bottom: 1px solid var(--color-border-default);
  background: var(--color-bg-elevated);
}

.profile-view__title {
  margin: 0;
  font-size: 16px;
  font-weight: 600;
  letter-spacing: 0.01em;
}

.profile-view__subtitle {
  margin: 4px 0 0;
  font-size: 12px;
  opacity: 0.7;
}

.profile-view__statusbar {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 16px;
  border-bottom: 1px solid var(--color-border-default);
  background: var(--color-bg-elevated);
  font-size: 12px;
}

.profile-view__badge {
  display: inline-flex;
  align-items: center;
  padding: 2px 8px;
  border-radius: 10px;
  font-size: 11px;
  font-weight: 500;
  letter-spacing: 0.02em;
}

.profile-view__badge--recording {
  background: var(--color-error-container);
  color: var(--color-error);
}

.profile-view__badge--recording::before {
  content: "●";
  margin-right: 4px;
  animation: profile-view-pulse 1.2s ease-in-out infinite;
}

.profile-view__badge--idle {
  background: var(--color-bg-surface-container);
  color: var(--color-text-secondary);
  opacity: 0.8;
}

.profile-view__kind {
  opacity: 0.85;
}

.profile-view__loading {
  opacity: 0.7;
}

.profile-view__body {
  flex: 1;
  min-height: 0;
  overflow: hidden;
  display: flex;
}

/* ProfilePanel 自身是 flex column，外层让它撑满 */
.profile-view__body > :deep(.profile-panel) {
  flex: 1;
  min-height: 0;
}

@keyframes profile-view-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.4; }
}

@media (prefers-reduced-motion: reduce) {
  .profile-view__badge--recording::before {
    animation: none;
  }
}
</style>
