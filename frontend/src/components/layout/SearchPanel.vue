<script setup lang="ts">
// Koyori IDE 组件 · Search Panel。
// 喵，这是 Search Panel，负责 Koyori IDE 的界面呈现喵~
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { appState } from "@/stores/app";
import {
  searchState,
  debouncedSearch,
  cancelDebouncedSearch,
  clearSearch,
  previewReplacements,
  applySelectedPreviews,
  cancelReplacePreview,
} from "@/stores/search";
import { openFileFromPath } from "@/stores/editor";
import { ElMessage } from "element-plus";
import { Search, Switch } from "@element-plus/icons-vue";
import { errorMessage } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import StructuralSearchView from "./StructuralSearchView.vue";

const { t } = useI18n();

const repoPath = computed(() => appState.currentProject ?? "");
const localQuery = ref(searchState.query);
const caseSensitive = ref(!searchState.ignoreCase);
const showReplace = ref(false);
const replaceText = ref("");
const replacing = ref(false);
const searchMode = ref<"text" | "symbol">("text");
const selectedPreviewCount = computed(() =>
  searchState.replacePreviews.filter((preview) => preview.selected).length,
);

const totalMatches = computed(() =>
  searchState.results.reduce((sum, r) => sum + r.matches.length, 0),
);

const hasResults = computed(() => searchState.results.length > 0);

function handleInput() {
  if (!repoPath.value) return;
  cancelReplacePreview();
  searchState.ignoreCase = !caseSensitive.value;
  debouncedSearch(repoPath.value, localQuery.value);
}

function handleClear() {
  localQuery.value = "";
  clearSearch();
}

async function handleMatchClick(filePath: string, line: number) {
  if (!repoPath.value) return;
  const fullPath = repoPath.value + "/" + filePath;
  await openFileFromPath(fullPath);
  // G-SEARCH-02: jump to the matching line in the editor (same pattern as Problems panel).
  appState.cursorLine = line;
  appState.cursorColumn = 1;
  appState.editorJumpSeq = (appState.editorJumpSeq || 0) + 1;
}

function toggleCaseSensitive() {
  caseSensitive.value = !caseSensitive.value;
  searchState.ignoreCase = !caseSensitive.value;
  if (localQuery.value) {
    handleInput();
  }
}

// G-SEARCH-01: toggle regex mode. When off, the query is treated as plain
// text (metacharacters are escaped before sending to the backend).
function toggleRegex() {
  searchState.useRegex = !searchState.useRegex;
  if (localQuery.value) {
    handleInput();
  }
}

async function handlePreviewReplace() {
  if (!localQuery.value || !repoPath.value) return;
  replacing.value = true;
  try {
    const pattern = searchState.useRegex ? localQuery.value : escapeSearchText(localQuery.value);
    await previewReplacements(repoPath.value, pattern, replaceText.value, caseSensitive.value);
  } catch (e: unknown) {
    ElMessage.error(t("search.replaceFailed", { error: errorMessage(e) }));
  } finally {
    replacing.value = false;
  }
}

async function handleApplyPreviews() {
  const pattern = searchState.useRegex ? localQuery.value : escapeSearchText(localQuery.value);
  const total = await applySelectedPreviews(pattern, replaceText.value, caseSensitive.value);
  if (searchState.error) {
    ElMessage.error(t("search.replaceFailed", { error: searchState.error }));
    return;
  }
  ElMessage.success(t("search.replacementsMade", { count: total }));
  handleInput();
}

function escapeSearchText(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function previewExcerpt(value: string): string {
  const lines = value.split(/\r?\n/);
  const excerpt = lines.slice(0, 20).join("\n");
  return lines.length > 20 ? `${excerpt}\n...` : excerpt;
}

watch(() => appState.currentProject, () => {
  localQuery.value = "";
  clearSearch();
});

onBeforeUnmount(cancelDebouncedSearch);
onBeforeUnmount(cancelReplacePreview);
</script>

<template>
  <div class="search-panel">
    <div class="search-panel__modes" role="tablist">
      <button
        type="button"
        role="tab"
        class="search-panel__mode"
        :class="{ active: searchMode === 'text' }"
        :aria-selected="searchMode === 'text'"
        @click="searchMode = 'text'"
      >
        {{ t("search.textMode") }}
      </button>
      <button
        type="button"
        role="tab"
        class="search-panel__mode"
        :class="{ active: searchMode === 'symbol' }"
        :aria-selected="searchMode === 'symbol'"
        @click="searchMode = 'symbol'"
      >
        {{ t("search.symbolMode") }}
      </button>
    </div>
    <template v-if="searchMode === 'text'">
    <!-- Search input -->
    <div class="search-panel__input-area">
      <div class="search-panel__input-wrap">
        <el-icon :size="12" class="search-panel__icon">
          <Search />
        </el-icon>
        <input
          v-model="localQuery"
          type="text"
          class="search-panel__input"
          :placeholder="t('search.placeholder')"
          :aria-label="t('search.queryAria')"
          @input="handleInput"
        />
        <button
          type="button"
          v-if="localQuery"
          class="search-panel__clear"
          :aria-label="t('search.clearAria')"
          @click="handleClear"
        >
          ×
        </button>
      </div>
      <button
        type="button"
        class="search-panel__case-btn"
        :class="{ 'search-panel__case-btn--active': caseSensitive }"
        :aria-pressed="caseSensitive"
        :aria-label="t('a11y.matchCase')"
        :title="t('search.matchCase')"
        @click="toggleCaseSensitive"
      >
        Aa
      </button>
      <button
        type="button"
        class="search-panel__case-btn"
        :class="{ 'search-panel__case-btn--active': searchState.useRegex }"
        :aria-pressed="searchState.useRegex"
        :aria-label="t('a11y.useRegularExpression')"
        :title="t('search.useRegex')"
        @click="toggleRegex"
      >
        .*
      </button>
      <button
        type="button"
        class="search-panel__toggle-replace"
        :class="{ active: showReplace }"
        @click="showReplace = !showReplace"
        :aria-label="t('search.toggleReplaceAria')"
        :title="t('search.toggleReplaceTitle')"
      >
        <el-icon :size="12"><Switch /></el-icon>
      </button>
    </div>

    <div class="search-panel__filters">
      <input
        v-model="searchState.includeGlob"
        class="search-panel__filter-input"
        :placeholder="t('search.includePlaceholder')"
        :aria-label="t('search.includeAria')"
        @input="handleInput"
      />
      <input
        v-model="searchState.excludeGlob"
        class="search-panel__filter-input"
        :placeholder="t('search.excludePlaceholder')"
        :aria-label="t('search.excludeAria')"
        @input="handleInput"
      />
    </div>

    <div v-if="showReplace" class="search-panel__replace-area">
      <input
        v-model="replaceText"
        class="search-panel__replace-input"
        :placeholder="t('search.replacePlaceholder')"
        @keydown.enter="handlePreviewReplace"
      />
      <button
        type="button"
        class="search-panel__replace-btn"
        :disabled="replacing || !hasResults"
        @click="handlePreviewReplace"
      >
        {{ replacing ? t('search.replaceInProgress') : t('search.previewReplace') }}
      </button>
    </div>

    <div v-if="searchState.replacePreviews.length" class="search-panel__replace-preview">
      <div class="search-panel__preview-actions">
        <span>{{ t('search.previewFiles', { count: searchState.replacePreviews.length }) }}</span>
        <button
          type="button"
          class="search-panel__preview-action"
          :disabled="searchState.previewApplying || selectedPreviewCount === 0"
          @click="handleApplyPreviews"
        >
          {{ t('search.applySelected', { count: selectedPreviewCount }) }}
        </button>
        <button type="button" class="search-panel__preview-action" @click="cancelReplacePreview">
          {{ t('search.cancelPreview') }}
        </button>
      </div>
      <div
        v-for="preview in searchState.replacePreviews"
        :key="preview.path"
        class="search-panel__preview-file"
      >
        <label class="search-panel__preview-file-heading">
          <input v-model="preview.selected" type="checkbox" />
          <span :title="preview.path">{{ preview.path }}</span>
          <small>{{ preview.replacements }}</small>
        </label>
        <div class="search-panel__preview-diff">
          <pre>{{ previewExcerpt(preview.originalContent) }}</pre>
          <pre>{{ previewExcerpt(preview.modifiedContent) }}</pre>
        </div>
      </div>
    </div>

    <!-- Summary -->
    <div v-if="hasResults && !searchState.loading" class="search-panel__summary">
      {{ t('search.summary', { files: searchState.results.length, matches: totalMatches }) }}
    </div>

    <!-- Loading -->
    <div v-if="searchState.loading" class="search-panel__loading">
      {{ t('search.searching') }}
    </div>

    <!-- Error -->
    <div v-if="searchState.error" class="search-panel__error">
      {{ searchState.error }}
    </div>

    <!-- Results -->
    <div v-if="hasResults && !searchState.loading" class="search-panel__results">
      <div v-for="result in searchState.results" :key="result.path" class="search-panel__file-group">
        <div class="search-panel__file-path" :title="result.path">
          {{ result.path }}
          <span class="search-panel__file-count">{{ result.matches.length }}</span>
        </div>
        <button
          type="button"
          v-for="match in result.matches"
          :key="`${result.path}:${match.line}:${match.column}`"
          class="search-panel__match"
          @click="handleMatchClick(result.path, match.line)"
        >
          <span class="search-panel__line-num">{{ match.line }}</span>
          <span class="search-panel__preview">{{ match.preview }}</span>
        </button>
      </div>
    </div>

    <!-- Empty state -->
    <div
      v-if="!hasResults && !searchState.loading && localQuery && !searchState.error"
      class="search-panel__empty"
    >
      {{ t('search.noResults') }}
    </div>
    <div
      v-if="!localQuery && !hasResults"
      class="search-panel__empty"
    >
      {{ t('search.typeToSearch') }}
    </div>
    </template>
    <StructuralSearchView
      v-else
      :repo-path="repoPath"
      :include-glob="searchState.includeGlob"
      :exclude-glob="searchState.excludeGlob"
      :case-sensitive="caseSensitive"
      @update:include-glob="searchState.includeGlob = $event"
      @update:exclude-glob="searchState.excludeGlob = $event"
      @update:case-sensitive="caseSensitive = $event"
    />
  </div>
</template>

<style scoped>
.search-panel {
  display: flex;
  flex-direction: column;
  height: 100%;
  font-family: var(--font-sans);
}

.search-panel__modes {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 2px;
  margin: 6px 12px 0;
  padding: 2px;
  border-radius: var(--radius-xs);
  background: var(--color-bg-surface-container-low);
}

.search-panel__mode {
  min-width: 0;
  height: 24px;
  border: 0;
  border-radius: var(--radius-xs);
  background: transparent;
  color: var(--color-text-tertiary);
  cursor: pointer;
  font-size: 11px;
}

.search-panel__mode.active {
  background: var(--color-bg-elevated);
  color: var(--color-text-primary);
}

.search-panel__input-area {
  display: flex;
  gap: 4px;
  padding: 8px 12px;
}

.search-panel__input-wrap {
  position: relative;
  flex: 1;
  display: flex;
  align-items: center;
}

.search-panel__icon {
  position: absolute;
  left: 8px;
  color: var(--color-text-tertiary);
  pointer-events: none;
}

.search-panel__input {
  width: 100%;
  padding: 8px 28px 8px 30px;
  font-size: 12px;
  font-family: var(--font-sans);
  color: var(--color-text-primary);
  background-color: var(--color-bg-elevated);
  border: 1px solid transparent;
  border-radius: var(--radius-md);
  outline: none;
  transition: border-color var(--transition-fast, 150ms var(--ease-standard));
}

.search-panel__input:focus {
  border-color: var(--color-primary);
}

.search-panel__clear {
  position: absolute;
  right: 4px;
  border: none;
  background: transparent;
  color: var(--color-text-tertiary);
  cursor: pointer;
  font-size: 16px;
  line-height: 1;
  padding: 2px 4px;
  border-radius: var(--radius-sm);
  transition: color var(--transition-fast, 150ms var(--ease-standard));
}

.search-panel__clear:hover {
  color: var(--color-text-primary);
}

.search-panel__case-btn {
  width: 32px;
  height: 32px;
  border: 1px solid var(--color-border-subtle);
  background: transparent;
  color: var(--color-text-tertiary);
  font-size: 11px;
  font-weight: 500;
  border-radius: var(--radius-sm);
  cursor: pointer;
  transition: background-color var(--transition-fast, 150ms var(--ease-standard)),
              border-color var(--transition-fast, 150ms var(--ease-standard)),
              color var(--transition-fast, 150ms var(--ease-standard));
}

.search-panel__case-btn--active {
  background: var(--color-primary-container);
  border-color: var(--color-primary);
  color: var(--color-primary);
}

.search-panel__toggle-replace {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  border: none;
  border-radius: var(--radius-xs);
  background: transparent;
  color: var(--color-text-tertiary);
  cursor: pointer;
}

.search-panel__toggle-replace:hover,
.search-panel__toggle-replace.active {
  color: var(--color-text-primary);
  background: var(--color-bg-surface-container-low);
}

.search-panel__replace-area {
  display: flex;
  gap: 4px;
  padding: 0 8px 6px;
}

.search-panel__filters {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: 4px;
  padding: 0 12px 6px;
}

.search-panel__filter-input {
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

.search-panel__filter-input:focus {
  border-color: var(--color-primary);
}

.search-panel__replace-preview {
  max-height: min(44vh, 440px);
  overflow: auto;
  border-block: 1px solid var(--color-border-subtle);
}

.search-panel__preview-actions {
  position: sticky;
  top: 0;
  z-index: 1;
  display: flex;
  align-items: center;
  gap: 5px;
  min-height: 32px;
  padding: 4px 8px;
  background: var(--color-bg-surface-dim);
  color: var(--color-text-secondary);
  font-size: 11px;
}

.search-panel__preview-actions span {
  flex: 1;
}

.search-panel__preview-action {
  height: 24px;
  padding: 0 8px;
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-xs);
  background: transparent;
  color: var(--color-text-primary);
  cursor: pointer;
  font-size: 11px;
}

.search-panel__preview-action:disabled {
  cursor: not-allowed;
  opacity: 0.5;
}

.search-panel__preview-file {
  padding: 6px 8px 8px;
  border-bottom: 1px solid var(--color-border-subtle);
}

.search-panel__preview-file-heading {
  display: grid;
  grid-template-columns: 16px minmax(0, 1fr) auto;
  align-items: center;
  gap: 5px;
  color: var(--color-text-secondary);
  font-size: 11px;
}

.search-panel__preview-file-heading span {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.search-panel__preview-diff {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: 4px;
  margin-top: 5px;
}

.search-panel__preview-diff pre {
  min-width: 0;
  max-height: 112px;
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

.search-panel__replace-input {
  flex: 1;
  height: 24px;
  padding: 0 8px;
  font-size: 12px;
  color: var(--color-text-primary);
  background: var(--color-bg-surface);
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-xs);
}

.search-panel__replace-btn {
  padding: 0 10px;
  height: 24px;
  font-size: 11px;
  color: var(--color-text-primary);
  background: var(--color-primary);
  border: none;
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.search-panel__replace-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.search-panel__summary,
.search-panel__loading,
.search-panel__empty,
.search-panel__error {
  padding: 4px 12px;
  font-size: 11px;
  color: var(--color-text-tertiary);
}

.search-panel__error {
  color: var(--color-error);
}

.search-panel__results {
  flex: 1;
  overflow-y: auto;
  padding-bottom: 8px;
}

.search-panel__file-group {
  margin-bottom: 4px;
}

.search-panel__file-path {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 6px 12px 2px;
  font-size: 11px;
  font-weight: 500;
  color: var(--color-text-secondary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.search-panel__file-count {
  flex-shrink: 0;
  padding: 1px 7px;
  font-size: 10px;
  color: var(--color-text-tertiary);
  background-color: var(--color-bg-surface-container);
  border-radius: var(--radius-sm);
}

.search-panel__match {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 4px 12px 4px 28px;
  background: transparent;
  border: none;
  border-radius: var(--radius-sm);
  cursor: pointer;
  text-align: left;
  transition: background-color var(--transition-fast, 150ms var(--ease-standard));
}

.search-panel__match:hover {
  background-color: var(--color-bg-surface-container-low);
}

.search-panel__line-num {
  flex-shrink: 0;
  width: 28px;
  font-size: 10px;
  color: var(--color-text-disabled);
  font-family: var(--font-mono);
  text-align: right;
}

.search-panel__preview {
  flex: 1;
  font-size: 11px;
  color: var(--color-text-primary);
  font-family: var(--font-mono);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
</style>
