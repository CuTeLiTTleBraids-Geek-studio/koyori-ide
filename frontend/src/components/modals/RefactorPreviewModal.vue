<script setup lang="ts">
// Koyori IDE 组件 · Refactor Preview Modal。
// 喵，这是 Refactor Preview Modal，负责 Koyori IDE 的界面呈现喵~
import { computed } from "vue";
import {
  applySelectedRefactor,
  cancelRefactorPreview,
  refactorState,
} from "@/stores/refactor";
import { useI18n } from "@/lib/i18n";
import FocusTrapDialog from "@/components/common/FocusTrapDialog.vue";

const { t } = useI18n();

const action = computed(() => refactorState.selectedAction);
const files = computed(() => action.value?.preview?.files ?? []);

function excerpt(content: string): string {
  const limit = 12_000;
  return content.length > limit ? `${content.slice(0, limit)}\n...` : content;
}
</script>

<template>
  <div
    v-if="refactorState.previewVisible && action"
    class="refactor-preview__backdrop"
  >
    <button
      type="button"
      class="dialog-backdrop-button"
      tabindex="-1"
      :aria-label="t('a11y.closeDialog')"
      @click="cancelRefactorPreview"
    />
    <FocusTrapDialog
      tag="section"
      class="refactor-preview"
      :aria-label="action.title"
      @close="cancelRefactorPreview"
    >
      <header class="refactor-preview__header">
        <h2>{{ action.title }}</h2>
        <span class="refactor-preview__count">{{
          t("refactor.fileCount", { count: files.length })
        }}</span>
      </header>

      <div class="refactor-preview__body">
        <div v-if="files.length === 0" class="refactor-preview__empty">
          {{ t("refactor.noTextEdits") }}
        </div>
        <article
          v-for="file in files"
          :key="file.filePath"
          class="refactor-preview__file"
        >
          <h3 :title="file.filePath">{{ file.filePath }}</h3>
          <div class="refactor-preview__diff">
            <section>
              <span>{{ t("refactor.before") }}</span>
              <pre>{{ excerpt(file.originalContent) }}</pre>
            </section>
            <section>
              <span>{{ t("refactor.after") }}</span>
              <pre>{{ excerpt(file.modifiedContent) }}</pre>
            </section>
          </div>
        </article>
      </div>

      <p
        v-if="refactorState.error"
        class="refactor-preview__error"
        role="alert"
      >
        {{ refactorState.error }}
      </p>

      <footer class="refactor-preview__actions">
        <button
          data-test="refactor-cancel"
          type="button"
          class="refactor-preview__button"
          @click="cancelRefactorPreview"
        >
          {{ t("refactor.cancel") }}
        </button>
        <button
          data-test="refactor-apply"
          type="button"
          class="refactor-preview__button refactor-preview__button--primary"
          :disabled="refactorState.applying"
          @click="applySelectedRefactor"
        >
          {{
            refactorState.applying
              ? t("refactor.applying")
              : t("refactor.apply")
          }}
        </button>
      </footer>
    </FocusTrapDialog>
  </div>
</template>

<style scoped>
.refactor-preview__backdrop {
  position: fixed;
  inset: 0;
  z-index: 1200;
  display: grid;
  place-items: center;
  padding: 24px;
  background: rgba(0, 0, 0, 0.48);
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

.refactor-preview {
  position: relative;
  z-index: 1;
  display: flex;
  flex-direction: column;
  width: min(1040px, 96vw);
  max-height: min(780px, 92vh);
  overflow: hidden;
  color: var(--color-text-primary);
  background: var(--color-bg-surface);
  border: 1px solid var(--color-border-default);
  border-radius: 8px;
  box-shadow: 0 16px 48px rgba(0, 0, 0, 0.35);
}

.refactor-preview__header,
.refactor-preview__actions {
  display: flex;
  align-items: center;
  flex: 0 0 auto;
  padding: 12px 16px;
}

.refactor-preview__header {
  justify-content: space-between;
  border-bottom: 1px solid var(--color-border-subtle);
}

.refactor-preview__header h2 {
  min-width: 0;
  margin: 0;
  overflow: hidden;
  font-size: 15px;
  line-height: 1.4;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.refactor-preview__count,
.refactor-preview__diff span {
  color: var(--color-text-tertiary);
  font-size: 11px;
}

.refactor-preview__body {
  min-height: 180px;
  overflow: auto;
}

.refactor-preview__file + .refactor-preview__file {
  border-top: 1px solid var(--color-border-subtle);
}

.refactor-preview__file h3 {
  margin: 0;
  padding: 10px 16px;
  overflow: hidden;
  font-family: var(--font-mono);
  font-size: 12px;
  font-weight: 500;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.refactor-preview__diff {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  border-top: 1px solid var(--color-border-subtle);
}

.refactor-preview__diff section {
  min-width: 0;
}

.refactor-preview__diff section + section {
  border-left: 1px solid var(--color-border-subtle);
}

.refactor-preview__diff span {
  display: block;
  padding: 6px 10px;
  background: var(--color-bg-subtle);
}

.refactor-preview__diff pre {
  min-height: 120px;
  margin: 0;
  padding: 10px;
  overflow: auto;
  font: 12px/1.5 var(--font-mono);
  white-space: pre;
}

.refactor-preview__empty {
  padding: 48px 16px;
  color: var(--color-text-tertiary);
  text-align: center;
}

.refactor-preview__error {
  flex: 0 0 auto;
  max-height: 96px;
  margin: 0;
  padding: 10px 16px;
  overflow: auto;
  color: var(--color-danger);
  border-top: 1px solid var(--color-border-subtle);
  font-size: 12px;
  white-space: pre-wrap;
}

.refactor-preview__actions {
  justify-content: flex-end;
  gap: 8px;
  border-top: 1px solid var(--color-border-subtle);
}

.refactor-preview__button {
  min-width: 76px;
  height: 30px;
  padding: 0 12px;
  color: var(--color-text-primary);
  background: transparent;
  border: 1px solid var(--color-border-default);
  border-radius: 4px;
  cursor: pointer;
}

.refactor-preview__button--primary {
  color: var(--color-primary-contrast, #fff);
  background: var(--color-primary);
  border-color: var(--color-primary);
}

.refactor-preview__button:disabled {
  cursor: default;
  opacity: 0.5;
}

@media (max-width: 720px) {
  .refactor-preview__backdrop {
    padding: 8px;
  }

  .refactor-preview__diff {
    grid-template-columns: minmax(0, 1fr);
  }

  .refactor-preview__diff section + section {
    border-top: 1px solid var(--color-border-subtle);
    border-left: 0;
  }
}
</style>
