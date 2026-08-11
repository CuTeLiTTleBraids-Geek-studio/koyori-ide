<script setup lang="ts">
// Koyori IDE 组件 · Debug View。
// 喵，这是 Debug View，负责 Koyori IDE 的界面呈现喵~
/**
 * F-10 (task-5.md): 独立调试视图 (/debug).
 *
 * 将 DebugPanel.vue 升级为完整全屏视图，提供：
 *   - 顶部调试工具栏（启动/继续/单步/停止/重启）
 *   - 左侧：变量面板 + Watch + Evaluate
 *   - 中间：调用栈 + 断点列表（函数断点/数据断点）
 *   - 右侧：调试控制台
 *
 * 复用 stores/debug 的全部状态与 action；DebugPanel 内部已实现这些能力，
 * 这里以全屏布局重新组织，并补充视图级标题与状态摘要。
 *
 * i18n 键前缀：view.debug.*
 */
import { onMounted } from "vue";
import DebugPanel from "@/components/layout/DebugPanel.vue";
import {
  debugState,
  refreshDebugStatus,
  loadLaunchConfigs,
} from "@/stores/debug";
import { useI18n } from "@/lib/i18n";
import { appState } from "@/stores/app";

const { t } = useI18n();

onMounted(() => {
  void loadLaunchConfigs(appState.currentProject ?? undefined);
  void refreshDebugStatus();
});
</script>

<template>
  <div class="debug-view" :aria-busy="debugState.busy">
    <header class="debug-view__header">
      <h1 class="debug-view__title">{{ t("view.debug.title") }}</h1>
      <p class="debug-view__subtitle">{{ t("view.debug.subtitle") }}</p>
    </header>

    <div v-if="debugState.busy" class="debug-view__state" role="status" aria-live="polite">
      {{ t("view.debug.loading") }}
    </div>
    <div v-else-if="debugState.lastError" class="debug-view__state debug-view__state--error" role="alert">
      {{ debugState.lastError }}
    </div>
    <div v-else-if="!debugState.running" class="debug-view__state" data-state="empty">
      {{ t("view.debug.idle") }}
    </div>

    <!--
      DebugPanel 已包含工具栏、多会话切换器、调用栈、Locals、Watch、Evaluate、
      断点列表（含函数断点 / 数据断点）、异常面板、StepIn target 选择菜单、
      调试控制台补全等全部调试能力。全屏视图直接复用，避免重复实现。
    -->
    <div class="debug-view__body">
      <DebugPanel :show-status="false" />
    </div>

    <!--
      视图级状态条：与 DebugPanel 内部状态条互补，提供视图层的运行/暂停摘要，
      便于用户从顶部一眼看到当前调试会话状态。
    -->
    <footer class="debug-view__footer" v-if="debugState.running">
      <span class="debug-view__badge debug-view__badge--running">
        {{ t("view.debug.statusRunning") }}
      </span>
      <span v-if="debugState.stopped" class="debug-view__badge debug-view__badge--paused">
        {{ t("view.debug.statusPaused") }}: {{ debugState.stopReason || "stopped" }}
      </span>
      <span v-if="debugState.activeConfigName" class="debug-view__config">
        · {{ debugState.activeConfigName }}
      </span>
    </footer>
  </div>
</template>

<style scoped>
.debug-view {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  background: var(--color-bg-base, #1e1e1e);
  color: var(--color-text-primary, #eee);
}

.debug-view__header {
  flex-shrink: 0;
  padding: 12px 16px;
  border-bottom: 1px solid var(--color-border, #333);
  background: var(--color-bg-elevated, #252526);
}

.debug-view__title {
  margin: 0;
  font-size: 16px;
  font-weight: 600;
  letter-spacing: 0.01em;
}

.debug-view__subtitle {
  margin: 4px 0 0;
  font-size: 12px;
  opacity: 0.7;
}

.debug-view__body {
  flex: 1;
  min-height: 0;
  min-width: 0;
  overflow: auto;
  display: flex;
}

.debug-view__state {
  flex-shrink: 0;
  padding: 7px 16px;
  border-bottom: 1px solid var(--color-border-default);
  color: var(--color-text-secondary);
  font-size: 12px;
}

.debug-view__state--error {
  background: var(--color-error-container);
  color: var(--color-error);
}

/* DebugPanel 自身是 flex column，外层让它撑满 */
.debug-view__body > :deep(.debug-panel) {
  flex: 1;
  min-height: 0;
}

.debug-view__footer {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 16px;
  border-top: 1px solid var(--color-border, #333);
  background: var(--color-bg-elevated, #252526);
  font-size: 12px;
}

.debug-view__badge {
  display: inline-flex;
  align-items: center;
  padding: 2px 8px;
  border-radius: 10px;
  font-size: 11px;
  font-weight: 500;
  letter-spacing: 0.02em;
}

.debug-view__badge--running {
  background: rgba(63, 185, 80, 0.15);
  color: #3fb950;
}

.debug-view__badge--running::before {
  content: "●";
  margin-right: 4px;
  animation: debug-view-pulse 1.2s ease-in-out infinite;
}

.debug-view__badge--paused {
  background: rgba(227, 179, 65, 0.15);
  color: #e3b341;
}

.debug-view__config {
  opacity: 0.7;
  font-family: var(--font-mono, ui-monospace, monospace);
}

@keyframes debug-view-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.4; }
}

@media (prefers-reduced-motion: reduce) {
  .debug-view__badge--running::before {
    animation: none;
  }
}
</style>
