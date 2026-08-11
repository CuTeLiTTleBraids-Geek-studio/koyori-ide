<script setup lang="ts">
// Koyori IDE 组件 · Structural Search View。
// 喵，这是 Structural Search View，负责 Koyori IDE 的界面呈现喵~
import { computed, onBeforeUnmount, watch } from "vue";
import { ElMessage } from "element-plus";
import { Check, Close, Search } from "@element-plus/icons-vue";
import { appState } from "@/stores/app";
import { openFileFromPath } from "@/stores/editor";
import {
  applySelectedStructuralPreviews,
  cancelStructuralPreview,
  cancelStructuralSearch,
  clearStructuralSearch,
  parseStructuralGlobInput,
  previewStructuralReplacements,
  runStructuralSearch,
  structuralSearchState,
} from "@/stores/structuralSearch";
import { useI18n } from "@/lib/i18n";

const props = defineProps<{
  repoPath: string;
  includeGlob: string;
  excludeGlob: string;
  caseSensitive: boolean;
}>();
const emit = defineEmits<{
  "update:includeGlob": [value: string];
  "update:excludeGlob": [value: string];
  "update:caseSensitive": [value: boolean];
}>();

const { t } = useI18n();
const selectedCount = computed(() => structuralSearchState.results.filter((result) => result.selected).length);
const selectedPreviewCount = computed(() => structuralSearchState.previews.filter((preview) => preview.selected).length);
const matchedFiles = computed(() => new Set(structuralSearchState.results.map((result) => result.path)).size);

async function handleSearch(): Promise<void> {
  await runStructuralSearch(props.repoPath, structuralSearchState.query, {
    caseSensitive: props.caseSensitive,
    includeGlobs: parseStructuralGlobInput(props.includeGlob),
    excludeGlobs: parseStructuralGlobInput(props.excludeGlob),
  });
}

function toggleAllResults(event: Event): void {
  const checked = (event.target as HTMLInputElement).checked;
  for (const result of structuralSearchState.results) result.selected = checked;
}

async function handleResultClick(path: string, line: number, character: number): Promise<void> {
  if (!props.repoPath) return;
  const fullPath = `${props.repoPath.replace(/[\\/]+$/, "")}/${path.replace(/^[\\/]+/, "")}`;
  await openFileFromPath(fullPath);
  appState.cursorLine = line + 1;
  appState.cursorColumn = character + 1;
  appState.editorJumpSeq = (appState.editorJumpSeq || 0) + 1;
}

async function handlePreview(): Promise<void> {
  await previewStructuralReplacements(props.repoPath, structuralSearchState.replacement);
  if (structuralSearchState.error) {
    ElMessage.error(t("search.replaceFailed", { error: structuralSearchState.error }));
  }
}

async function handleApply(): Promise<void> {
  const total = await applySelectedStructuralPreviews();
  if (structuralSearchState.error) {
    ElMessage.error(t("search.replaceFailed", { error: structuralSearchState.error }));
    return;
  }
  ElMessage.success(t("search.replacementsMade", { count: total }));
  await handleSearch();
}

function previewExcerpt(value: string): string {
  const lines = value.split(/\r?\n/);
  const excerpt = lines.slice(0, 16).join("\n");
  return lines.length > 16 ? `${excerpt}\n...` : excerpt;
}

watch(() => props.repoPath, clearStructuralSearch);
onBeforeUnmount(cancelStructuralSearch);
onBeforeUnmount(cancelStructuralPreview);
</script>

<template>
  <div class="structural-search-view">
    <div class="structural-search-view__query-row">
      <span class="structural-search-view__badge" :title="t('search.structuralScopeTitle')">LSP</span>
      <input
        v-model="structuralSearchState.query"
        class="structural-search-view__input"
        type="text"
        :placeholder="t('search.structuralPatternPlaceholder')"
        :aria-label="t('search.structuralQueryAria')"
        @keydown.enter="handleSearch"
      />
      <button
        type="button"
        class="structural-search-view__case-button"
        :class="{ active: caseSensitive }"
        :aria-pressed="caseSensitive"
        :aria-label="t('a11y.matchCase')"
        :title="t('search.matchCase')"
        @click="emit('update:caseSensitive', !caseSensitive)"
      >
        Aa
      </button>
      <button
        type="button"
        class="structural-search-view__icon-button"
        :title="t('search.structuralSearch')"
        :aria-label="t('search.structuralSearch')"
        :disabled="structuralSearchState.loading || !structuralSearchState.query.trim()"
        @click="handleSearch"
      >
        <el-icon :size="14"><Search /></el-icon>
      </button>
    </div>

    <div class="structural-search-view__filters">
      <input
        :value="includeGlob"
        class="structural-search-view__filter-input"
        :placeholder="t('search.includePlaceholder')"
        :aria-label="t('search.includeAria')"
        @input="emit('update:includeGlob', ($event.target as HTMLInputElement).value)"
      />
      <input
        :value="excludeGlob"
        class="structural-search-view__filter-input"
        :placeholder="t('search.excludePlaceholder')"
        :aria-label="t('search.excludeAria')"
        @input="emit('update:excludeGlob', ($event.target as HTMLInputElement).value)"
      />
    </div>

    <div class="structural-search-view__replace-row">
      <input
        v-model="structuralSearchState.replacement"
        class="structural-search-view__input"
        type="text"
        :placeholder="t('search.structuralReplacementPlaceholder')"
        @keydown.enter="handlePreview"
      />
      <button
        type="button"
        class="structural-search-view__command"
        :disabled="structuralSearchState.previewLoading || selectedCount === 0 || !structuralSearchState.replacement.trim()"
        @click="handlePreview"
      >
        {{ t("search.previewReplace") }}
      </button>
    </div>

    <div v-if="structuralSearchState.previews.length" class="structural-search-view__previews">
      <div class="structural-search-view__preview-actions">
        <span>{{ t("search.previewFiles", { count: structuralSearchState.previews.length }) }}</span>
        <button
          type="button"
          class="structural-search-view__icon-button"
          :disabled="structuralSearchState.applying || selectedPreviewCount === 0"
          :aria-label="t('search.applySelected', { count: selectedPreviewCount })"
          :title="t('search.applySelected', { count: selectedPreviewCount })"
          @click="handleApply"
        >
          <el-icon :size="14"><Check /></el-icon>
        </button>
        <button
          type="button"
          class="structural-search-view__icon-button"
          :disabled="structuralSearchState.applying"
          :aria-label="t('search.cancelPreview')"
          :title="t('search.cancelPreview')"
          @click="cancelStructuralPreview"
        >
          <el-icon :size="14"><Close /></el-icon>
        </button>
      </div>
      <div v-for="preview in structuralSearchState.previews" :key="preview.path" class="structural-search-view__preview">
        <label class="structural-search-view__preview-heading">
          <input v-model="preview.selected" type="checkbox" />
          <span :title="preview.path">{{ preview.path }}</span>
          <small>{{ preview.replacements }}</small>
        </label>
        <div class="structural-search-view__diff">
          <pre>{{ previewExcerpt(preview.originalContent) }}</pre>
          <pre>{{ previewExcerpt(preview.modifiedContent) }}</pre>
        </div>
      </div>
    </div>

    <div v-if="structuralSearchState.loading" class="structural-search-view__state">
      {{ t("search.searching") }}
    </div>
    <div v-else-if="structuralSearchState.error" class="structural-search-view__state structural-search-view__state--error">
      {{ structuralSearchState.error }}
    </div>
    <template v-else-if="structuralSearchState.results.length">
      <div class="structural-search-view__summary">
        <label>
          <input
            type="checkbox"
            :checked="selectedCount === structuralSearchState.results.length"
            :aria-label="t('search.structuralSelectAll')"
            @change="toggleAllResults"
          />
          <span>{{ t("search.structuralSummary", { matches: structuralSearchState.results.length, files: matchedFiles }) }}</span>
        </label>
        <span v-if="structuralSearchState.truncated" :title="t('search.structuralTruncated')">500+</span>
      </div>
      <div class="structural-search-view__results">
        <div
          v-for="result in structuralSearchState.results"
          :key="`${result.path}:${result.selectionRange.start.line}:${result.selectionRange.start.character}`"
          class="structural-search-view__result"
        >
          <input v-model="result.selected" type="checkbox" :aria-label="result.name" />
          <button
            type="button"
            class="structural-search-view__result-target"
            @click="handleResultClick(result.path, result.selectionRange.start.line, result.selectionRange.start.character)"
          >
            <span class="structural-search-view__kind">{{ result.kindLabel }}</span>
            <span class="structural-search-view__symbol-path">{{ result.symbolPath.join(" > ") }}</span>
            <small>{{ result.path }}:{{ result.selectionRange.start.line + 1 }}</small>
          </button>
        </div>
      </div>
    </template>
    <div v-else-if="structuralSearchState.query" class="structural-search-view__state">
      {{ t("search.structuralNoResults") }}
      <small v-if="structuralSearchState.skippedFiles">
        {{ t("search.structuralSkipped", { count: structuralSearchState.skippedFiles }) }}
      </small>
    </div>
    <div v-else class="structural-search-view__state">
      {{ t("search.structuralTypeToSearch") }}
    </div>
  </div>
</template>

<style scoped>
.structural-search-view {
  display: flex;
  flex: 1;
  width: 100%;
  min-width: 0;
  min-height: 0;
  flex-direction: column;
}

.structural-search-view__query-row,
.structural-search-view__replace-row {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 6px 12px 0;
}

.structural-search-view__filters {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: 4px;
  padding: 4px 12px 0;
}

.structural-search-view__filter-input {
  min-width: 0;
  height: 24px;
  padding: 0 7px;
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-xs);
  outline: none;
  background: var(--color-bg-surface);
  color: var(--color-text-primary);
  font-size: 11px;
}

.structural-search-view__filter-input:focus {
  border-color: var(--color-primary);
}

.structural-search-view__replace-row {
  padding-top: 4px;
  padding-bottom: 6px;
}

.structural-search-view__badge {
  flex: 0 0 auto;
  padding: 2px 4px;
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-xs);
  color: var(--color-text-secondary);
  font-size: 9px;
  font-weight: 600;
}

.structural-search-view__input {
  min-width: 0;
  height: 28px;
  flex: 1;
  padding: 0 8px;
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-xs);
  outline: none;
  background: var(--color-bg-elevated);
  color: var(--color-text-primary);
  font-family: var(--font-mono);
  font-size: 11px;
}

.structural-search-view__input:focus {
  border-color: var(--color-primary);
}

.structural-search-view__icon-button,
.structural-search-view__command,
.structural-search-view__case-button {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  height: 28px;
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-xs);
  background: transparent;
  color: var(--color-text-primary);
  cursor: pointer;
}

.structural-search-view__case-button {
  width: 28px;
  padding: 0;
  font-size: 10px;
}

.structural-search-view__case-button.active {
  border-color: var(--color-primary);
  background: var(--color-primary-container);
  color: var(--color-primary);
}

.structural-search-view__icon-button {
  width: 28px;
  padding: 0;
}

.structural-search-view__command {
  padding: 0 8px;
  font-size: 11px;
}

.structural-search-view__icon-button:disabled,
.structural-search-view__command:disabled {
  cursor: not-allowed;
  opacity: 0.5;
}

.structural-search-view__state,
.structural-search-view__summary {
  padding: 7px 12px;
  color: var(--color-text-tertiary);
  font-size: 11px;
}

.structural-search-view__state {
  display: flex;
  flex-direction: column;
  gap: 3px;
}

.structural-search-view__state--error {
  color: var(--color-error);
}

.structural-search-view__summary {
  display: flex;
  align-items: center;
  justify-content: space-between;
  border-top: 1px solid var(--color-border-subtle);
}

.structural-search-view__summary label,
.structural-search-view__preview-heading {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 6px;
}

.structural-search-view__results,
.structural-search-view__previews {
  min-height: 0;
  overflow: auto;
  border-top: 1px solid var(--color-border-subtle);
}

.structural-search-view__result {
  display: grid;
  grid-template-columns: 18px minmax(0, 1fr);
  align-items: center;
  padding: 3px 8px;
}

.structural-search-view__result:hover {
  background: var(--color-bg-surface-container-low);
}

.structural-search-view__result-target {
  display: grid;
  min-width: 0;
  grid-template-columns: auto minmax(0, 1fr);
  gap: 2px 6px;
  padding: 3px 0;
  border: 0;
  background: transparent;
  color: var(--color-text-primary);
  cursor: pointer;
  text-align: left;
}

.structural-search-view__kind {
  align-self: start;
  padding: 1px 4px;
  border-radius: var(--radius-xs);
  background: var(--color-bg-surface-container);
  color: var(--color-text-secondary);
  font-size: 9px;
}

.structural-search-view__symbol-path,
.structural-search-view__result-target small,
.structural-search-view__preview-heading span {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.structural-search-view__symbol-path {
  font-family: var(--font-mono);
  font-size: 11px;
}

.structural-search-view__result-target small {
  grid-column: 2;
  color: var(--color-text-tertiary);
  font-size: 10px;
}

.structural-search-view__preview-actions {
  position: sticky;
  top: 0;
  z-index: 1;
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 4px 8px;
  background: var(--color-bg-surface-dim);
  color: var(--color-text-secondary);
  font-size: 11px;
}

.structural-search-view__preview-actions span {
  flex: 1;
}

.structural-search-view__preview {
  padding: 6px 8px;
  border-top: 1px solid var(--color-border-subtle);
}

.structural-search-view__preview-heading {
  display: grid;
  grid-template-columns: 16px minmax(0, 1fr) auto;
  color: var(--color-text-secondary);
  font-size: 10px;
}

.structural-search-view__diff {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: 4px;
  margin-top: 5px;
}

.structural-search-view__diff pre {
  min-width: 0;
  max-height: 120px;
  margin: 0;
  padding: 5px;
  overflow: auto;
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-xs);
  background: var(--color-bg-base);
  color: var(--color-text-primary);
  font-family: var(--font-mono);
  font-size: 10px;
  line-height: 1.35;
  white-space: pre;
}
</style>
