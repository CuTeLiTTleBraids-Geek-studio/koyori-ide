<script setup lang="ts">
// Koyori IDE 组件 · Context Chips。
// 喵，这是 Context Chips，负责 Koyori IDE 的界面呈现喵~
// Plan 11 Task 1/3 — right-side collapsible context panel. Lists all context
// attached to the next message: legacy mentionedFiles + Task 3 contextChips
// (@mention + paste). Each chip can be removed individually. The panel is
// collapsible (48px when collapsed, 320px when expanded) via aiAssistantState.
import { useI18n } from "@/lib/i18n";
import { aiState, removeMentionedFile, removeContextChip } from "@/stores/ai";
import { aiAssistantState, toggleContextPanel } from "@/stores/aiAssistant";

const { t } = useI18n();
</script>

<template>
  <aside
    class="ai-context"
    :class="{ 'ai-context--collapsed': aiAssistantState.contextPanelCollapsed }"
  >
    <div class="ai-context__header">
      <span class="ai-context__title">{{ t("aiAssistant.context") }}</span>
      <button
        type="button"
        class="ai-context__toggle"
        :aria-label="aiAssistantState.contextPanelCollapsed
          ? t('a11y.expandContextPanel')
          : t('a11y.collapseContextPanel')"
        @click="toggleContextPanel"
      >
        {{ aiAssistantState.contextPanelCollapsed ? "◀" : "▶" }}
      </button>
    </div>
    <div v-if="!aiAssistantState.contextPanelCollapsed" class="ai-context__body">
      <div
        v-if="aiState.mentionedFiles.length === 0 && aiState.contextChips.length === 0"
        class="ai-context__empty"
      >
        {{ t("aiAssistant.noContext") }}
      </div>
      <!-- Legacy mentioned files (runAIAction / selection) -->
      <div
        v-for="f in aiState.mentionedFiles"
        :key="f.filePath"
        class="ai-context__chip"
      >
        <code>{{ f.filePath }}</code>
        <button
          type="button"
          class="ai-context__remove"
          :title="t('common.remove')"
          :aria-label="t('a11y.removeContextItem', { name: f.filePath })"
          @click="removeMentionedFile(f.filePath)"
        >×</button>
      </div>
      <!-- Task 3 context chips (@mention + paste) -->
      <div
        v-for="chip in aiState.contextChips"
        :key="chip.id"
        class="ai-context__chip"
        :class="`ai-context__chip--${chip.kind}`"
      >
        <img
          v-if="chip.kind === 'image' && chip.imageUrl"
          :src="chip.imageUrl"
          :alt="chip.label"
          class="ai-context__chip-img"
        />
        <span class="ai-context__chip-kind">{{ chip.kind }}</span>
        <span class="ai-context__chip-label">{{ chip.label }}</span>
        <button
          type="button"
          class="ai-context__remove"
          :title="t('common.remove')"
          :aria-label="t('a11y.removeContextItem', { name: chip.label })"
          @click="removeContextChip(chip.id)"
        >×</button>
      </div>
    </div>
  </aside>
</template>

<style scoped>
.ai-context {
  width: 320px;
  border-left: 1px solid var(--color-border-subtle, #2a2a2a);
  background: var(--color-bg-surface, #fafafc);
  flex-shrink: 0;
  overflow: hidden;
  transition: width 0.15s ease;
}
.ai-context--collapsed {
  width: 48px;
}
.ai-context__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 12px;
  border-bottom: 1px solid var(--color-border-subtle, #2a2a2a);
}
.ai-context--collapsed .ai-context__title {
  display: none;
}
.ai-context__title {
  font-size: 12px;
  color: var(--color-text-secondary, #aaa);
}
.ai-context__toggle {
  border: none;
  background: transparent;
  color: var(--color-text-secondary, #aaa);
  cursor: pointer;
}
.ai-context__body {
  padding: 8px 12px;
}
.ai-context__empty {
  font-size: 12px;
  color: var(--color-text-secondary, #888);
}
.ai-context__chip {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 4px 8px;
  margin-bottom: 4px;
  font-size: 11px;
  background: var(--color-bg-elevated, #f5f5f7);
  border-radius: 4px;
}
.ai-context__chip-img {
  max-width: 40px;
  max-height: 28px;
  border-radius: 2px;
}
.ai-context__chip-kind {
  font-size: 9px;
  font-weight: 600;
  text-transform: uppercase;
  color: var(--color-accent, #3b82f6);
  min-width: 40px;
}
.ai-context__chip-label {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--color-text-primary, #e0e0e0);
}
.ai-context__chip code {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ai-context__remove {
  border: none;
  background: transparent;
  color: var(--color-text-secondary, #888);
  cursor: pointer;
  font-size: 14px;
  line-height: 1;
  padding: 0 2px;
}
.ai-context__remove:hover {
  color: var(--color-danger, #ef4444);
}
</style>
