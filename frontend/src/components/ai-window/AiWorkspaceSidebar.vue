<script setup lang="ts">
// Koyori IDE 组件 · Ai Workspace Sidebar。
// 喵，这是 Ai Workspace Sidebar，负责 Koyori IDE 的界面呈现喵~
import {
  ChatDotRound,
  Clock,
  MagicStick,
  Monitor,
  Setting,
} from "@element-plus/icons-vue";
import ConversationSidebar from "@/components/ai-assistant/ConversationSidebar.vue";
import { useDragResize } from "@/composables/useDragResize";
import { useI18n } from "@/lib/i18n";
import {
  AI_SIDEBAR_MAX,
  AI_SIDEBAR_MIN,
  type AIWorkspaceView,
} from "@/stores/aiWindow";

const props = defineProps<{
  activeView: AIWorkspaceView;
  width: number;
  terminalVisible: boolean;
}>();

const emit = defineEmits<{
  (e: "select-view", view: AIWorkspaceView): void;
  (e: "select-conversation", id: string): void;
  (e: "toggle-terminal"): void;
  (e: "resize", width: number): void;
  (e: "resize-commit", width: number): void;
}>();

const { t } = useI18n();

const items: Array<{
  view: AIWorkspaceView;
  icon: typeof ChatDotRound;
  labelKey: string;
  descriptionKey: string;
}> = [
  { view: "assistant", icon: ChatDotRound, labelKey: "aiWorkspace.assistant", descriptionKey: "aiWorkspace.assistantDesc" },
  { view: "skills", icon: MagicStick, labelKey: "aiWorkspace.skills", descriptionKey: "aiWorkspace.skillsDesc" },
  { view: "automation", icon: Clock, labelKey: "aiWorkspace.automation", descriptionKey: "aiWorkspace.automationDesc" },
  { view: "settings", icon: Setting, labelKey: "aiWorkspace.settings", descriptionKey: "aiWorkspace.settingsDesc" },
  { view: "rollback", icon: Clock, labelKey: "aiWorkspace.rollback", descriptionKey: "aiWorkspace.rollbackDesc" },
];

const resize = useDragResize({
  direction: "horizontal",
  sign: "positive-increases",
  min: AI_SIDEBAR_MIN,
  max: AI_SIDEBAR_MAX,
  getStartSize: () => props.width,
  onResize: (width) => emit("resize", width),
  onCommit: (width) => emit("resize-commit", width),
});
</script>

<template>
  <aside class="ai-workspace-sidebar" :style="{ width: `${width}px` }">
    <nav class="ai-workspace-sidebar__nav" :aria-label="t('aiWindow.activityAria')">
      <button
        v-for="item in items"
        :key="item.view"
        type="button"
        class="ai-workspace-sidebar__nav-item"
        :class="{ 'is-active': activeView === item.view }"
        :data-view="item.view"
        :aria-current="activeView === item.view ? 'page' : undefined"
        @click="emit('select-view', item.view)"
      >
        <el-icon :size="19"><component :is="item.icon" /></el-icon>
        <span class="ai-workspace-sidebar__nav-copy">
          <strong>{{ t(item.labelKey) }}</strong>
          <small>{{ t(item.descriptionKey) }}</small>
        </span>
      </button>
      <button
        type="button"
        class="ai-workspace-sidebar__nav-item ai-workspace-sidebar__terminal"
        :class="{ 'is-active': terminalVisible }"
        :aria-pressed="terminalVisible"
        @click="emit('toggle-terminal')"
      >
        <el-icon :size="19"><Monitor /></el-icon>
        <span class="ai-workspace-sidebar__nav-copy">
          <strong>{{ t("aiWorkspace.terminal") }}</strong>
          <small>{{ t("aiWorkspace.terminalDesc") }}</small>
        </span>
      </button>
    </nav>

    <div class="ai-workspace-sidebar__conversations">
      <ConversationSidebar
        :width="width"
        embedded
        @select="emit('select-conversation', $event)"
      />
    </div>

    <div
      class="ai-workspace-sidebar__resize"
      role="separator"
      tabindex="0"
      aria-orientation="vertical"
      :aria-label="t('aiWorkspace.resizeSidebar')"
      :aria-valuemin="resize.ariaMin"
      :aria-valuemax="resize.ariaMax"
      :aria-valuenow="width"
      @pointerdown="resize.onPointerDown"
      @keydown="resize.onKeyDown"
    />
  </aside>
</template>

<style scoped>
.ai-workspace-sidebar {
  position: relative;
  display: flex;
  flex-direction: column;
  flex: 0 0 auto;
  min-width: 260px;
  max-width: 380px;
  height: 100%;
  overflow: hidden;
  background: var(--color-sidebar-bg);
  border-right: 1px solid var(--color-border-default);
}

.ai-workspace-sidebar__nav {
  display: grid;
  gap: 4px;
  padding: 10px;
  border-bottom: 1px solid var(--color-border-subtle);
}

.ai-workspace-sidebar__nav-item {
  position: relative;
  display: grid;
  grid-template-columns: 24px minmax(0, 1fr);
  align-items: center;
  gap: 9px;
  min-height: 48px;
  padding: 7px 9px;
  border: 0;
  border-radius: var(--radius-sm);
  color: var(--chrome-text-secondary);
  background: transparent;
  text-align: left;
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast), transform var(--transition-fast);
}

.ai-workspace-sidebar__nav-item:hover {
  color: var(--chrome-text-primary);
  background: var(--chrome-hover-bg);
}

.ai-workspace-sidebar__nav-item:active {
  transform: scale(0.98);
}

.ai-workspace-sidebar__nav-item.is-active {
  color: var(--chrome-text-active);
  background: var(--chrome-active-bg);
}

.ai-workspace-sidebar__nav-item.is-active::before {
  content: "";
  position: absolute;
  inset: 9px auto 9px 0;
  width: 2px;
  border-radius: var(--radius-pill);
  background: currentColor;
}

.ai-workspace-sidebar__nav-copy {
  display: grid;
  min-width: 0;
  gap: 1px;
}

.ai-workspace-sidebar__nav-copy strong {
  font-size: 13px;
  font-weight: 600;
  line-height: 1.25;
}

.ai-workspace-sidebar__nav-copy small {
  overflow: hidden;
  color: var(--color-text-tertiary);
  font-size: 10px;
  line-height: 1.35;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ai-workspace-sidebar__terminal {
  margin-top: 2px;
  border-top: 1px solid var(--color-border-subtle);
  border-radius: 0 0 var(--radius-sm) var(--radius-sm);
}

.ai-workspace-sidebar__conversations {
  flex: 1;
  min-height: 0;
  overflow: hidden;
}

.ai-workspace-sidebar__resize {
  position: absolute;
  z-index: 4;
  inset: 0 -2px 0 auto;
  width: 5px;
  cursor: col-resize;
  outline: none;
}

.ai-workspace-sidebar__resize:hover,
.ai-workspace-sidebar__resize:focus-visible {
  background: color-mix(in srgb, var(--color-primary) 55%, transparent);
}

@media (prefers-reduced-motion: reduce) {
  .ai-workspace-sidebar__nav-item { transition: none; }
  .ai-workspace-sidebar__nav-item:active { transform: none; }
}
</style>
