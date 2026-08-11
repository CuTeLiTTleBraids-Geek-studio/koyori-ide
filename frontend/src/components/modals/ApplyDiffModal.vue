<script setup lang="ts">
// Koyori IDE 组件 · Apply Diff Modal。
// 喵，这是 Apply Diff Modal，负责 Koyori IDE 的界面呈现喵~
/**
 * prompt-5 Task A / BUG-H2 — global Diff preview for apply-to-editor.
 * Mounted on the main window layout so AI companion window apply requests
 * always surface a confirmable Diff before writing the buffer.
 */
import { computed } from "vue";
import { VueMonacoDiffEditor } from "@guolao/vue-monaco-editor";
import { Close } from "@element-plus/icons-vue";
import { applyDiffState, cancelApplyDiff, confirmApplyDiff } from "@/stores/editor";
import { appState } from "@/stores/app";
import { getMonacoThemeName } from "@/lib/monaco-themes";
import { useI18n } from "@/lib/i18n";
import FocusTrapDialog from "@/components/common/FocusTrapDialog.vue";

const { t } = useI18n();

const diffMonacoTheme = computed(() => getMonacoThemeName(appState.accentTheme));

const fileName = computed(
  () => applyDiffState.path.split(/[/\\]/).pop() ?? applyDiffState.path,
);

async function onConfirm(): Promise<void> {
  await confirmApplyDiff();
}
</script>

<template>
  <transition name="fade">
    <div
      v-if="applyDiffState.visible"
      class="apply-diff-overlay"
    >
      <button
        type="button"
        class="dialog-backdrop-button"
        tabindex="-1"
        :aria-label="t('a11y.closeDialog')"
        @click="cancelApplyDiff"
      />
      <FocusTrapDialog
        class="apply-diff-modal"
        :aria-label="t('aiChat.applyCodeLabel')"
        @close="cancelApplyDiff"
      >
        <div class="apply-diff-modal__header">
          <span class="apply-diff-modal__title">
            {{ t("aiChat.applyToTitle", { name: fileName }) }}
          </span>
          <button
            type="button"
            class="apply-diff-modal__close"
            :aria-label="t('aiChat.closeDiffPreview')"
            @click="cancelApplyDiff"
          >
            <el-icon :size="14"><Close /></el-icon>
          </button>
        </div>
        <div class="apply-diff-modal__body">
          <VueMonacoDiffEditor
            :original="applyDiffState.original"
            :modified="applyDiffState.modified"
            :language="applyDiffState.language"
            :theme="diffMonacoTheme"
            :options="{
              readOnly: true,
              fontSize: appState.fontSize,
              fontFamily: appState.fontFamily,
              minimap: { enabled: false },
              renderSideBySide: true,
              automaticLayout: true,
            }"
            height="100%"
          />
        </div>
        <div class="apply-diff-modal__footer">
          <button
            type="button"
            class="apply-diff-modal__btn apply-diff-modal__btn--secondary"
            @click="cancelApplyDiff"
          >
            {{ t("common.cancel") }}
          </button>
          <button
            type="button"
            class="apply-diff-modal__btn apply-diff-modal__btn--primary"
            @click="onConfirm"
          >
            {{ t("aiChat.applyToFile") }}
          </button>
        </div>
      </FocusTrapDialog>
    </div>
  </transition>
</template>

<style scoped>
.apply-diff-overlay {
  position: fixed;
  inset: 0;
  background-color: color-mix(in srgb, var(--color-surface-black) 50%, transparent);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 2000;
  padding: var(--space-lg);
}

.dialog-backdrop-button {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  margin: 0;
  padding: 0;
  border: 0;
  background: transparent;
  cursor: default;
}

.apply-diff-modal {
  position: relative;
  z-index: 1;
  display: flex;
  flex-direction: column;
  width: min(900px, 95vw);
  height: min(640px, 88vh);
  background-color: var(--color-canvas);
  border: 1px solid var(--color-hairline);
  border-radius: var(--radius-lg);
  overflow: hidden;
}

.apply-diff-modal__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: var(--space-sm) var(--space-md);
  border-bottom: 1px solid var(--color-hairline);
  background-color: var(--color-canvas-parchment);
}

.apply-diff-modal__title {
  font-family: var(--font-sans);
  font-size: 14px;
  font-weight: 600;
  line-height: 1.29;
  letter-spacing: -0.224px;
  color: var(--color-text-primary);
}

.apply-diff-modal__close {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border: none;
  border-radius: var(--radius-full);
  background: transparent;
  color: var(--color-text-tertiary);
  cursor: pointer;
}

.apply-diff-modal__close:hover {
  color: var(--color-text-primary);
  background-color: var(--color-bg-surface-container-high);
}

.apply-diff-modal__body {
  flex: 1;
  min-height: 0;
}

.apply-diff-modal__footer {
  display: flex;
  justify-content: flex-end;
  gap: var(--space-xs);
  padding: var(--space-sm) var(--space-md);
  border-top: 1px solid var(--color-hairline);
  background-color: var(--color-canvas-parchment);
}

.apply-diff-modal__btn {
  padding: 8px var(--space-md);
  font-family: var(--font-sans);
  font-size: 14px;
  font-weight: 400;
  line-height: 1.29;
  border-radius: var(--radius-pill);
  cursor: pointer;
}

.apply-diff-modal__btn--secondary {
  background: transparent;
  color: var(--color-primary);
  border: 1px solid var(--color-primary);
}

.apply-diff-modal__btn--primary {
  background: var(--color-primary);
  color: var(--color-on-primary);
  border: 1px solid var(--color-primary);
}

.fade-enter-active,
.fade-leave-active {
  transition: opacity 150ms ease;
}
.fade-enter-from,
.fade-leave-to {
  opacity: 0;
}
</style>
