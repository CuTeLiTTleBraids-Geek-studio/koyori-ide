<script setup lang="ts">
// Koyori IDE 组件 · Ai Assistant View。
// 喵，这是 Ai Assistant View，负责 Koyori IDE 的界面呈现喵~
// Plan 11 Task 1 — AI 助手独立页面主视图。
// 三栏布局：左 ConversationSidebar（可拖拽宽度）+ 中 MessageList+InputComposer
// + 右 ContextChips（可折叠）。顶部 AssistantHeader 含模式切换/模型/返回。
// 与嵌入式 AiChatPanel 共享 aiState，切换不丢失会话。
// Task 16 Step 11: 快捷键体系（Ctrl+L 聚焦 / Ctrl+Shift+L 新会话 / Ctrl+Enter 发送）。
import { computed, ref } from "vue";
import AssistantHeader from "@/components/ai-assistant/AssistantHeader.vue";
import ConversationSidebar from "@/components/ai-assistant/ConversationSidebar.vue";
import MessageList from "@/components/ai-assistant/MessageList.vue";
import InputComposer from "@/components/ai-assistant/InputComposer.vue";
import ContextChips from "@/components/ai-assistant/ContextChips.vue";
import PlanPanel from "@/components/ai-assistant/PlanPanel.vue";
import { aiAssistantState, setSidebarWidth } from "@/stores/aiAssistant";
import { aiState, loadConversation, clearMessages } from "@/stores/ai";
import { registerShortcut, unregisterShortcut } from "@/composables/useKeyboard";
import { useI18n } from "@/lib/i18n";
import { onBeforeUnmount } from "vue";

const { t } = useI18n();

const sidebarWidth = computed(() => aiAssistantState.sidebarWidth);

// InputComposer ref 用于快捷键聚焦输入框与触发发送。
const composer = ref<InstanceType<typeof InputComposer> | null>(null);

// 拖拽调整侧栏宽度。mousedown 后监听 mousemove/mouseup。
let dragging = false;
function onResizeStart(e: MouseEvent): void {
  e.preventDefault();
  dragging = true;
  const onMove = (ev: MouseEvent): void => {
    if (!dragging) return;
    setSidebarWidth(ev.clientX);
  };
  const onUp = (): void => {
    dragging = false;
    window.removeEventListener("mousemove", onMove);
    window.removeEventListener("mouseup", onUp);
  };
  window.addEventListener("mousemove", onMove);
  window.addEventListener("mouseup", onUp);
}

async function handleSelectConversation(id: string): Promise<void> {
  if (id === "") {
    // 新会话 — Task 2 接入 createConversation。
    aiState.currentConversationId = null;
    aiState.currentConversationTitle = null;
    aiState.messages = [];
    return;
  }
  aiState.currentConversationId = id;
  await loadConversation(id);
}

// Task 16 Step 11: 快捷键
const focusShortcut = {
  key: "l",
  ctrl: true,
  label: t("shortcuts.aiFocusInput"),
  handler: () => {
    const el = (composer.value as unknown as { textareaRef?: { value?: HTMLTextAreaElement } } | null)?.textareaRef?.value;
    el?.focus();
  },
};
const newConvShortcut = {
  key: "l",
  ctrl: true,
  shift: true,
  label: t("shortcuts.aiNewConversation"),
  handler: () => {
    aiState.currentConversationId = null;
    aiState.currentConversationTitle = null;
    clearMessages();
  },
};
const sendShortcut = {
  key: "enter",
  ctrl: true,
  label: t("shortcuts.aiSendMessage"),
  handler: () => {
    (composer.value as unknown as { handleSend?: () => void } | null)?.handleSend?.();
  },
};

registerShortcut(focusShortcut);
registerShortcut(newConvShortcut);
registerShortcut(sendShortcut);
onBeforeUnmount(() => {
  unregisterShortcut(focusShortcut);
  unregisterShortcut(newConvShortcut);
  unregisterShortcut(sendShortcut);
});
</script>

<template>
  <div class="ai-assistant-view">
    <AssistantHeader />
    <div class="ai-assistant-view__body">
      <ConversationSidebar
        :width="sidebarWidth"
        @select="handleSelectConversation"
      />
      <div
        class="ai-assistant-view__resize"
        @mousedown="onResizeStart"
      />
      <div class="ai-assistant-view__main">
        <transition name="ai-view-switch" mode="out-in">
          <MessageList :key="aiState.currentConversationId ?? 'new'" />
        </transition>
        <InputComposer ref="composer" />
      </div>
      <!-- BUG4: plan 模式右侧显示 PlanPanel，其他模式显示 ContextChips -->
      <PlanPanel v-if="aiAssistantState.mode === 'plan'" />
      <ContextChips v-else />
    </div>
  </div>
</template>

<style scoped>
.ai-assistant-view {
  display: flex;
  flex-direction: column;
  height: 100vh;
  width: 100vw;
  background: var(--color-bg-base, #1e1e1e);
  color: var(--color-text-primary, #e0e0e0);
  overflow: hidden;
}
.ai-assistant-view__body {
  display: flex;
  flex: 1;
  overflow: hidden;
}
.ai-assistant-view__resize {
  width: 4px;
  cursor: col-resize;
  background: transparent;
  flex-shrink: 0;
}
.ai-assistant-view__resize:hover {
  background: var(--color-accent, #3b82f6);
}
.ai-assistant-view__main {
  display: flex;
  flex-direction: column;
  flex: 1;
  overflow: hidden;
}

/* 建议二: AI 页面切换过渡动画 — fade + 12px 横向滑动 */
.ai-view-switch-enter-active,
.ai-view-switch-leave-active {
  transition: opacity 0.18s ease, transform 0.18s ease;
}
.ai-view-switch-enter-from {
  opacity: 0;
  transform: translateX(12px);
}
.ai-view-switch-leave-to {
  opacity: 0;
  transform: translateX(-12px);
}

@media (prefers-reduced-motion: reduce) {
  .ai-view-switch-enter-active,
  .ai-view-switch-leave-active {
    transition: none;
  }
}
</style>
