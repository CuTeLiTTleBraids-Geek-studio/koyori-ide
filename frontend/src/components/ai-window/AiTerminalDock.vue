<script setup lang="ts">
// Koyori IDE 组件 · Ai Terminal Dock。
// 喵，这是 Ai Terminal Dock，负责 Koyori IDE 的界面呈现喵~
import { Close } from "@element-plus/icons-vue";
import TerminalPanel from "@/components/layout/TerminalPanel.vue";
import { useDragResize } from "@/composables/useDragResize";
import { useI18n } from "@/lib/i18n";
import { AI_TERMINAL_MIN } from "@/stores/aiWindow";
import { ref, onBeforeUnmount, nextTick } from "vue";

const props = defineProps<{
  visible: boolean;
  width: number;
  maxWidth: number;
}>();

const emit = defineEmits<{
  (e: "close"): void;
  (e: "resize", width: number): void;
  (e: "resize-commit", width: number): void;
}>();

const { t } = useI18n();

const resize = useDragResize({
  direction: "horizontal",
  sign: "positive-decreases",
  min: AI_TERMINAL_MIN,
  max: props.maxWidth,
  getStartSize: () => props.width,
  onResize: (width) => emit("resize", width),
  onCommit: (width) => emit("resize-commit", width),
});

// BUG4a: 持有 TerminalPanel 的 ref，在过渡动画结束后调用 fitTerminal()
// 重新计算 xterm 列/行。不这样做的话，xterm 会在 width:0 的容器中初始化，
// 导致键盘输入被静默丢弃。
const terminalPanelRef = ref<InstanceType<typeof TerminalPanel> | null>(null);
let resizeObserver: ResizeObserver | null = null;

function onAfterEnter() {
  // 过渡动画完成后，重新 fit xterm。
  nextTick(() => {
    terminalPanelRef.value?.fitTerminal();
    // BUG4a: 设置 ResizeObserver 监听 dock 宽度变化（如拖拽调整宽度），
    // 自动 refit xterm。清理旧的 observer 避免重复。
    if (resizeObserver) {
      resizeObserver.disconnect();
    }
    const el = document.querySelector(".ai-terminal-dock__body");
    if (el && typeof ResizeObserver !== "undefined") {
      resizeObserver = new ResizeObserver(() => {
        terminalPanelRef.value?.fitTerminal();
      });
      resizeObserver.observe(el);
    }
  });
}

function onBeforeLeave() {
  // 清理 ResizeObserver 避免内存泄漏。
  if (resizeObserver) {
    resizeObserver.disconnect();
    resizeObserver = null;
  }
}

onBeforeUnmount(() => {
  onBeforeLeave();
});
</script>

<template>
  <transition name="ai-terminal-dock" @after-enter="onAfterEnter" @before-leave="onBeforeLeave">
    <aside
      v-if="visible"
      class="ai-terminal-dock"
      :style="{ width: `${width}px` }"
      :aria-label="t('aiWorkspace.terminal')"
    >
      <div
        class="ai-terminal-dock__resize"
        role="separator"
        tabindex="0"
        aria-orientation="vertical"
        :aria-label="t('aiWorkspace.resizeTerminal')"
        :aria-valuemin="resize.ariaMin"
        :aria-valuemax="maxWidth"
        :aria-valuenow="width"
        @pointerdown="resize.onPointerDown"
        @keydown="resize.onKeyDown"
      />
      <div class="ai-terminal-dock__header">
        <strong>{{ t("aiWorkspace.terminal") }}</strong>
        <button
          type="button"
          class="ai-terminal-dock__close"
          data-action="close-terminal"
          :aria-label="t('terminal.closePanelAria')"
          @click="emit('close')"
        >
          <el-icon :size="16"><Close /></el-icon>
        </button>
      </div>
      <div class="ai-terminal-dock__body">
        <TerminalPanel ref="terminalPanelRef" embedded @close="emit('close')" />
      </div>
    </aside>
  </transition>
</template>

<style scoped>
.ai-terminal-dock {
  position: relative;
  display: flex;
  flex: 0 0 auto;
  flex-direction: column;
  min-width: 340px;
  height: 100%;
  overflow: hidden;
  background: var(--color-terminal-bg);
  border-left: 1px solid var(--color-border-default);
}

.ai-terminal-dock__resize {
  position: absolute;
  z-index: 5;
  inset: 0 auto 0 -2px;
  width: 5px;
  cursor: col-resize;
  outline: none;
}

.ai-terminal-dock__resize:hover,
.ai-terminal-dock__resize:focus-visible {
  background: color-mix(in srgb, var(--color-primary) 55%, transparent);
}

.ai-terminal-dock__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  height: 42px;
  padding: 0 10px 0 14px;
  color: var(--color-text-secondary);
  background: var(--color-bg-surface);
  border-bottom: 1px solid var(--color-border-subtle);
  font-size: 12px;
}

.ai-terminal-dock__close {
  display: grid;
  width: 30px;
  height: 30px;
  place-items: center;
  border: 0;
  border-radius: var(--radius-sm);
  color: inherit;
  background: transparent;
  cursor: pointer;
}

.ai-terminal-dock__close:hover {
  color: var(--color-text-primary);
  background: var(--chrome-hover-bg);
}

.ai-terminal-dock__body {
  flex: 1;
  min-height: 0;
  overflow: hidden;
}

.ai-terminal-dock__body :deep(.terminal-panel) {
  position: static;
  width: 100%;
  height: 100% !important;
  border-top: 0;
}

.ai-terminal-dock-enter-active,
.ai-terminal-dock-leave-active {
  transition: opacity 220ms var(--ease-standard), width 240ms var(--ease-standard);
}

.ai-terminal-dock-enter-from,
.ai-terminal-dock-leave-to {
  width: 0 !important;
  opacity: 0;
}

@media (prefers-reduced-motion: reduce) {
  .ai-terminal-dock-enter-active,
  .ai-terminal-dock-leave-active { transition: none; }
}
</style>
