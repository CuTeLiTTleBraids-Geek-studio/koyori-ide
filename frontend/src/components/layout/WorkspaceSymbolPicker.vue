<script setup lang="ts">
// Koyori IDE 组件 · Workspace Symbol Picker。
// 喵，这是 Workspace Symbol Picker，负责 Koyori IDE 的界面呈现喵~
import { computed, nextTick, onBeforeUnmount, ref, watch } from "vue";
import {
  Box,
  Collection,
  DataLine,
  Document,
  Files,
  Key,
  Operation,
} from "@element-plus/icons-vue";
import { requestEditorJump } from "@/stores/app";
import { openFileFromPath } from "@/stores/editor";
import { layoutState } from "@/stores/layout";
import { getWorkspaceSymbols } from "@/stores/lsp";
import type { WorkspaceSymbol } from "@/types";
import { errorMessage } from "@/lib/errors";
import { notifyError } from "@/lib/notifications";
import { basename, dirname, matchIndices, scoreMatch } from "@/lib/fuzzy";
import { useI18n } from "@/lib/i18n";

const props = defineProps<{
  visible: boolean;
}>();

const emit = defineEmits<{
  (e: "close"): void;
}>();

const { t } = useI18n();

const ITEM_HEIGHT = 54;
const VIEWPORT_HEIGHT = 360;
const OVERSCAN = 4;
const query = ref("");
const selectedIndex = ref(0);
const inputRef = ref<HTMLInputElement | null>(null);
const dialogRef = ref<HTMLElement | null>(null);
const listRef = ref<HTMLElement | null>(null);
const loading = ref(false);
const results = ref<WorkspaceSymbol[]>([]);
const scrollTop = ref(0);
let debounceTimer: ReturnType<typeof setTimeout> | null = null;
let previouslyFocused: HTMLElement | null = null;
let requestGeneration = 0;

const visibleRange = computed(() => {
  const start = Math.max(0, Math.floor(scrollTop.value / ITEM_HEIGHT) - OVERSCAN);
  const count = Math.ceil(VIEWPORT_HEIGHT / ITEM_HEIGHT) + OVERSCAN * 2;
  return { start, end: Math.min(results.value.length, start + count) };
});

const visibleResults = computed(() =>
  results.value
    .slice(visibleRange.value.start, visibleRange.value.end)
    .map((symbol, offset) => ({ symbol, index: visibleRange.value.start + offset })),
);

const virtualHeight = computed(() => `${results.value.length * ITEM_HEIGHT}px`);

function normalizedPath(path: string): string {
  return path.replace(/\\/g, "/");
}

function symbolKindIcon(kind: number) {
  if (kind === 1) return Document;
  if (kind >= 2 && kind <= 4) return Files;
  if ([5, 10, 11, 23].includes(kind)) return Box;
  if ([6, 9, 12, 25].includes(kind)) return Operation;
  if ([7, 8, 13, 14, 20, 22].includes(kind)) return Key;
  if ([15, 16, 17, 18, 19, 21].includes(kind)) return Collection;
  return DataLine;
}

function symbolPath(symbol: WorkspaceSymbol): string {
  const uri = symbol.location.uri;
  if (!uri.toLowerCase().startsWith("file://")) return uri;
  try {
    const url = new URL(uri);
    let path = decodeURIComponent(url.pathname);
    if (/^\/[a-zA-Z]:\//.test(path)) path = path.slice(1);
    return path.replace(/\//g, "\\");
  } catch {
    return decodeURIComponent(uri.replace(/^file:\/\/+/, ""));
  }
}

function symbolLine(symbol: WorkspaceSymbol): number {
  return symbol.location.range.start.line;
}

function symbolColumn(symbol: WorkspaceSymbol): number {
  return symbol.location.range.start.character;
}

function symbolKey(symbol: WorkspaceSymbol): string {
  return `${symbol.location.uri}:${symbolLine(symbol)}:${symbolColumn(symbol)}:${symbol.name}`;
}

async function searchSymbols(rawQuery: string) {
  const trimmedQuery = rawQuery.trim();
  const generation = ++requestGeneration;
  if (!trimmedQuery) {
    results.value = [];
    loading.value = false;
    return;
  }

  loading.value = true;
  try {
    const symbols = await getWorkspaceSymbols(trimmedQuery);
    if (generation !== requestGeneration) return;

    results.value = symbols
      .map((symbol) => {
        const searchText = `${symbol.name} ${symbol.containerName ?? ""} ${symbol.location.uri}`;
        const indices = matchIndices(searchText, trimmedQuery);
        return { symbol, score: indices === null ? 0 : scoreMatch(searchText, indices) };
      })
      .sort((left, right) => right.score - left.score || left.symbol.name.localeCompare(right.symbol.name))
      .map(({ symbol }) => symbol);
    selectedIndex.value = 0;
    scrollTop.value = 0;
    if (listRef.value) listRef.value.scrollTop = 0;
  } catch {
    if (generation === requestGeneration) results.value = [];
  } finally {
    if (generation === requestGeneration) loading.value = false;
  }
}

watch(
  () => props.visible,
  (visible) => {
    if (visible) {
      previouslyFocused = document.activeElement as HTMLElement | null;
      query.value = "";
      selectedIndex.value = 0;
      results.value = [];
      scrollTop.value = 0;
      nextTick(() => inputRef.value?.focus());
      return;
    }

    requestGeneration += 1;
    if (debounceTimer) {
      clearTimeout(debounceTimer);
      debounceTimer = null;
    }
    loading.value = false;
    previouslyFocused?.focus?.();
    previouslyFocused = null;
  },
  { immediate: true },
);

watch(query, (value) => {
  selectedIndex.value = 0;
  requestGeneration += 1;
  if (debounceTimer) clearTimeout(debounceTimer);
  debounceTimer = setTimeout(() => {
    debounceTimer = null;
    void searchSymbols(value);
  }, 300);
});

function ensureSelectionVisible() {
  const list = listRef.value;
  if (!list) return;
  const top = selectedIndex.value * ITEM_HEIGHT;
  const bottom = top + ITEM_HEIGHT;
  if (top < list.scrollTop) list.scrollTop = top;
  else if (bottom > list.scrollTop + list.clientHeight) {
    list.scrollTop = bottom - list.clientHeight;
  }
}

async function openSelected(symbol: WorkspaceSymbol) {
  try {
    const path = symbolPath(symbol);
    await openFileFromPath(path);
    requestEditorJump(
      path,
      symbolLine(symbol) + 1,
      symbolColumn(symbol) + 1,
      layoutState.tree.activeLeafId,
    );
  } catch (error) {
    notifyError(errorMessage(error));
  } finally {
    emit("close");
  }
}

function handleKeydown(event: KeyboardEvent) {
  if (event.key === "ArrowDown") {
    event.preventDefault();
    if (results.value.length === 0) return;
    selectedIndex.value = Math.min(selectedIndex.value + 1, results.value.length - 1);
    nextTick(ensureSelectionVisible);
  } else if (event.key === "ArrowUp") {
    event.preventDefault();
    if (results.value.length === 0) return;
    selectedIndex.value = Math.max(selectedIndex.value - 1, 0);
    nextTick(ensureSelectionVisible);
  } else if (event.key === "Enter") {
    event.preventDefault();
    const symbol = results.value[selectedIndex.value];
    if (symbol) void openSelected(symbol);
  } else if (event.key === "Escape") {
    emit("close");
  }
}

function handleScroll(event: Event) {
  scrollTop.value = (event.currentTarget as HTMLElement).scrollTop;
}

function handleTab(event: KeyboardEvent) {
  const root = dialogRef.value;
  if (!root) return;
  const focusable = Array.from(
    root.querySelectorAll<HTMLElement>(
      'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])',
    ),
  ).filter((element) =>
    !element.hasAttribute("disabled")
    && element.getAttribute("aria-hidden") !== "true"
    && !element.hasAttribute("hidden")
    && !element.closest("[inert]")
    && element.tabIndex >= 0
  );
  if (focusable.length === 0) return;
  const first = focusable[0];
  const last = focusable[focusable.length - 1];
  if (event.shiftKey && (document.activeElement === first || document.activeElement === root)) {
    event.preventDefault();
    last.focus();
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault();
    first.focus();
  }
}

onBeforeUnmount(() => {
  requestGeneration += 1;
  if (debounceTimer) clearTimeout(debounceTimer);
});
</script>

<template>
  <transition name="qo-fade">
    <div
      v-if="visible"
      class="workspace-symbol-overlay"
    >
      <button
        type="button"
        class="dialog-backdrop-button"
        tabindex="-1"
        :aria-label="t('a11y.closeDialog')"
        @click="emit('close')"
      />
      <div
        ref="dialogRef"
        class="workspace-symbol-picker"
        role="dialog"
        aria-modal="true"
        :aria-label="t('mainLayout.workspaceSymbols')"
        tabindex="-1"
        @keydown.tab="handleTab"
      >
        <input
          ref="inputRef"
          v-model="query"
          class="workspace-symbol-picker__input"
          :placeholder="t('mainLayout.searchSymbols')"
          :aria-label="t('workspaceSymbols.inputAria')"
          role="combobox"
          aria-expanded="true"
          :aria-activedescendant="results[selectedIndex] ? `ws-item-${selectedIndex}` : undefined"
          @keydown="handleKeydown"
        />

        <div
          v-if="loading"
          class="workspace-symbol-picker__status"
          role="status"
          aria-live="polite"
        >
          {{ t("workspaceSymbols.searching") }}
        </div>
        <div
          v-else-if="query.trim() && results.length === 0"
          class="workspace-symbol-picker__status"
          role="status"
          aria-live="polite"
        >
          {{ t("workspaceSymbols.noResults") }}
        </div>
        <div
          v-else-if="!query.trim()"
          class="workspace-symbol-picker__status"
          role="status"
        >
          {{ t("workspaceSymbols.typeToSearch") }}
        </div>

        <div
          v-else
          ref="listRef"
          class="workspace-symbol-picker__list"
          role="listbox"
          :aria-label="t('mainLayout.workspaceSymbols')"
          aria-live="polite"
          @scroll="handleScroll"
        >
          <div class="workspace-symbol-picker__virtual" :style="{ height: virtualHeight }">
            <button
              v-for="entry in visibleResults"
              :id="`ws-item-${entry.index}`"
              :key="symbolKey(entry.symbol)"
              type="button"
              class="workspace-symbol-picker__item"
              :class="{ 'workspace-symbol-picker__item--active': entry.index === selectedIndex }"
              :style="{ transform: `translateY(${entry.index * ITEM_HEIGHT}px)` }"
              role="option"
              tabindex="-1"
              :aria-selected="entry.index === selectedIndex"
              :aria-label="`${entry.symbol.name}, ${symbolPath(entry.symbol)}:${symbolLine(entry.symbol) + 1}`"
              @click="openSelected(entry.symbol)"
              @mouseenter="selectedIndex = entry.index"
            >
              <el-icon class="workspace-symbol-picker__kind" aria-hidden="true">
                <component :is="symbolKindIcon(entry.symbol.kind)" />
              </el-icon>
              <span class="workspace-symbol-picker__body">
                <span class="workspace-symbol-picker__row">
                  <span class="workspace-symbol-picker__name">{{ entry.symbol.name }}</span>
                  <span v-if="entry.symbol.containerName" class="workspace-symbol-picker__container">
                    {{ entry.symbol.containerName }}
                  </span>
                </span>
                <span class="workspace-symbol-picker__meta">
                  <span>{{ basename(normalizedPath(symbolPath(entry.symbol))) }}:{{ symbolLine(entry.symbol) + 1 }}</span>
                  <span>{{ dirname(normalizedPath(symbolPath(entry.symbol))) }}</span>
                </span>
              </span>
            </button>
          </div>
        </div>
      </div>
    </div>
  </transition>
</template>

<style scoped>
.workspace-symbol-overlay {
  position: fixed;
  inset: 0;
  z-index: 1000;
  display: flex;
  justify-content: center;
  align-items: flex-start;
  padding-top: 80px;
  background-color: rgba(0, 0, 0, 0.4);
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

.workspace-symbol-picker {
  position: relative;
  z-index: 1;
  width: min(620px, 90vw);
  overflow: hidden;
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-md);
  background-color: var(--color-bg-surface);
  box-shadow: 0 8px 32px rgba(0, 0, 0, 0.3);
}

.workspace-symbol-picker__input {
  width: 100%;
  padding: 12px 16px;
  border: 0;
  border-bottom: 1px solid var(--color-border-subtle);
  outline: none;
  color: var(--color-text-primary);
  background: transparent;
  font-family: var(--font-sans);
  font-size: 14px;
}

.workspace-symbol-picker__input::placeholder {
  color: var(--color-text-tertiary);
}

.workspace-symbol-picker__status {
  padding: 18px 16px;
  color: var(--color-text-tertiary);
  font-size: 12px;
  text-align: center;
}

.workspace-symbol-picker__list {
  position: relative;
  height: 360px;
  overflow-y: auto;
  padding: 4px;
}

.workspace-symbol-picker__virtual {
  position: relative;
}

.workspace-symbol-picker__item {
  position: absolute;
  top: 0;
  left: 0;
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  height: 54px;
  padding: 7px 12px;
  border: 0;
  border-radius: var(--radius-sm);
  color: var(--color-text-primary);
  background: transparent;
  cursor: pointer;
  text-align: left;
}

.workspace-symbol-picker__item:hover,
.workspace-symbol-picker__item--active {
  background-color: color-mix(in srgb, var(--color-primary) 12%, transparent);
}

.workspace-symbol-picker__kind {
  flex: 0 0 18px;
  color: var(--color-primary);
}

.workspace-symbol-picker__body {
  min-width: 0;
  flex: 1;
}

.workspace-symbol-picker__row,
.workspace-symbol-picker__meta {
  display: flex;
  align-items: baseline;
  min-width: 0;
}

.workspace-symbol-picker__row {
  gap: 8px;
}

.workspace-symbol-picker__name {
  overflow: hidden;
  font-size: 13px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.workspace-symbol-picker__container {
  overflow: hidden;
  color: var(--color-text-secondary);
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.workspace-symbol-picker__meta {
  justify-content: space-between;
  gap: 12px;
  margin-top: 3px;
  color: var(--color-text-tertiary);
  font-family: var(--font-mono);
  font-size: 10px;
}

.workspace-symbol-picker__meta span:last-child {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

@media (prefers-reduced-motion: reduce) {
  .qo-fade-enter-active,
  .qo-fade-leave-active {
    transition: none;
  }
}
</style>
