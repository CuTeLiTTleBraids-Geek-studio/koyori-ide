<script setup lang="ts">
// Koyori IDE 组件 · Outline Panel。
// 喵，这是 Outline Panel，负责 Koyori IDE 的界面呈现喵~
import { computed, onBeforeUnmount, ref, shallowRef, watch, type Component } from "vue";
import {
  ArrowDown,
  ArrowRight,
  DataAnalysis,
  Document,
  Fold,
  Folder,
  Grid,
  Key,
  Operation,
  RefreshRight,
  Search,
} from "@element-plus/icons-vue";
import { appState } from "@/stores/app";
import { activeFile } from "@/stores/editor";
import { getLSPDocumentSymbols } from "@/stores/lsp";
import { useI18n } from "@/lib/i18n";
import type { LSPDocumentSymbol, LSPPosition, LSPRange } from "@/types";

interface OutlineRow {
  id: string;
  depth: number;
  symbol: LSPDocumentSymbol;
  hasChildren: boolean;
}

/**
 * One node of the symbol tree, flattened into DFS order (GOAL-P0-08).
 *
 * The tree used to be walked recursively on every render *and* on every cursor
 * move. Clicking a symbol moves the cursor, which fed a second full walk, so a
 * large tree paid repeated O(n) recursion per click. Flattening once per symbol
 * payload turns every later consumer into a single linear pass.
 */
interface FlatNode {
  id: string;
  depth: number;
  symbol: LSPDocumentSymbol;
  hasChildren: boolean;
  /** Index of this node's parent in the same array, or -1 for a root. */
  parent: number;
  /** Lowercased name + detail, precomputed so filtering never re-lowercases. */
  haystack: string;
}

/**
 * Depth ceiling for symbol-tree traversal.
 *
 * A malformed or cyclic `children` graph (a symbol reachable from itself) made
 * every recursive walk non-terminating, which hard-hangs the renderer — no
 * amount of debouncing helps once the stack is spinning. Real symbol trees are
 * nowhere near this deep, so the ceiling is a safety net, not a product limit.
 */
const MAX_SYMBOL_DEPTH = 64;

/**
 * Row ceiling. Beyond this the panel reports truncation instead of building an
 * unbounded DOM list, so a pathological document degrades visibly rather than
 * freezing the window.
 */
const MAX_OUTLINE_ROWS = 5000;

/** Debounce for content-driven refreshes. Path/language changes stay immediate. */
const SYMBOL_DEBOUNCE_MS = 300;

const { t } = useI18n();
const documentSymbols = shallowRef<LSPDocumentSymbol[]>([]);
const expandedIds = ref<Set<string>>(new Set());
const filterQuery = ref("");
const loading = ref(false);
let symbolRequest = 0;
let debounceTimer: ReturnType<typeof setTimeout> | null = null;
let loadedPath = "";
let loadedContent = "";

/**
 * Flattens the symbol tree with cycle and size defense.
 *
 * `onPath` holds the ancestors of the node being visited. A symbol that is its
 * own ancestor is skipped rather than followed, which is what makes a cyclic
 * payload terminate. Using an ancestor set (not a global "seen" set) keeps a
 * symbol object that legitimately appears in two sibling subtrees intact.
 */
function flattenSymbols(roots: LSPDocumentSymbol[]): { nodes: FlatNode[]; truncated: boolean } {
  const nodes: FlatNode[] = [];
  const onPath = new Set<LSPDocumentSymbol>();
  let truncated = false;

  function walk(
    symbols: LSPDocumentSymbol[],
    depth: number,
    parentId: string,
    parent: number,
  ): void {
    if (depth > MAX_SYMBOL_DEPTH) {
      truncated = true;
      return;
    }
    for (let index = 0; index < symbols.length; index += 1) {
      if (nodes.length >= MAX_OUTLINE_ROWS) {
        truncated = true;
        return;
      }
      const symbol = symbols[index];
      // A server may emit a null hole or a nameless entry; neither can be
      // rendered or jumped to, so drop it instead of throwing during render.
      if (!symbol || typeof symbol.name !== "string") continue;
      if (onPath.has(symbol)) {
        truncated = true;
        continue;
      }
      const id = parentId ? `${parentId}.${index}` : String(index);
      const children = Array.isArray(symbol.children) ? symbol.children : [];
      const self = nodes.length;
      nodes.push({
        id,
        depth,
        symbol,
        hasChildren: children.length > 0,
        parent,
        haystack: `${symbol.name}\n${symbol.detail ?? ""}`.toLocaleLowerCase(),
      });
      if (children.length > 0) {
        onPath.add(symbol);
        walk(children, depth + 1, id, self);
        onPath.delete(symbol);
      }
    }
  }

  walk(roots, 0, "", -1);
  return { nodes, truncated };
}

const flatIndex = computed(() => flattenSymbols(documentSymbols.value));
const outlineTruncated = computed(() => flatIndex.value.truncated);

const visibleRows = computed<OutlineRow[]>(() => {
  const { nodes } = flatIndex.value;
  const query = filterQuery.value.trim().toLocaleLowerCase();
  const filtering = query.length > 0;

  // A single reverse pass propagates a descendant match up to its ancestors.
  // The previous implementation re-scanned each node's whole subtree, making
  // filtering O(n^2) on wide trees.
  let keep: boolean[] | null = null;
  if (filtering) {
    keep = new Array<boolean>(nodes.length).fill(false);
    for (let i = nodes.length - 1; i >= 0; i -= 1) {
      const node = nodes[i]!;
      if (node.haystack.includes(query)) keep[i] = true;
      if (keep[i] && node.parent >= 0) keep[node.parent] = true;
    }
  }

  const rows: OutlineRow[] = [];
  // Nodes whose ancestor is collapsed or filtered out are themselves hidden.
  // DFS order guarantees a parent is decided before any of its children.
  const hidden = new Set<number>();
  for (let i = 0; i < nodes.length; i += 1) {
    const node = nodes[i]!;
    if (node.parent >= 0 && hidden.has(node.parent)) {
      hidden.add(i);
      continue;
    }
    if (keep && !keep[i]) {
      hidden.add(i);
      continue;
    }
    rows.push({
      id: node.id,
      depth: node.depth,
      symbol: node.symbol,
      hasChildren: node.hasChildren,
    });
    if (node.hasChildren && !(filtering || expandedIds.value.has(node.id))) hidden.add(i);
  }
  return rows;
});

function branchIdsFrom(nodes: FlatNode[]): Set<string> {
  const ids = new Set<string>();
  for (const node of nodes) if (node.hasChildren) ids.add(node.id);
  return ids;
}

async function loadSymbols(): Promise<void> {
  const file = activeFile.value;
  const request = ++symbolRequest;
  if (!file?.path || !file.language) {
    documentSymbols.value = [];
    expandedIds.value = new Set();
    loadedPath = "";
    loadedContent = "";
    loading.value = false;
    return;
  }

  // Switching files invalidates expansion state. Editing the *same* file must
  // not collapse the tree the user just expanded, which is what clearing on
  // every keystroke did.
  const samePath = loadedPath === file.path;
  if (!samePath) {
    documentSymbols.value = [];
    expandedIds.value = new Set();
  }

  loading.value = true;
  let symbols: LSPDocumentSymbol[] = [];
  try {
    symbols = await getLSPDocumentSymbols(file.language, file.path, file.content);
  } catch {
    // A symbol failure must leave the panel usable. Leaving `loading` set made
    // the spinner permanent, with no way back short of switching files.
    if (request === symbolRequest) loading.value = false;
    return;
  }
  if (request !== symbolRequest || activeFile.value?.path !== file.path) return;

  documentSymbols.value = Array.isArray(symbols) ? symbols : [];
  const branches = branchIdsFrom(flattenSymbols(documentSymbols.value).nodes);
  // On a same-path refresh keep only expansions that still name a branch, so a
  // stale id from a previous parse cannot resurrect a row.
  expandedIds.value = samePath
    ? new Set([...expandedIds.value].filter((id) => branches.has(id)))
    : branches;
  loadedPath = file.path;
  loadedContent = file.content;
  loading.value = false;
}

function scheduleLoad(immediate: boolean): void {
  if (debounceTimer) {
    clearTimeout(debounceTimer);
    debounceTimer = null;
  }
  if (immediate) {
    void loadSymbols();
    return;
  }
  debounceTimer = setTimeout(() => {
    debounceTimer = null;
    const file = activeFile.value;
    // An immediate path-change load may already have covered this content.
    if (file && file.path === loadedPath && file.content === loadedContent) return;
    void loadSymbols();
  }, SYMBOL_DEBOUNCE_MS);
}

// Opening or switching a file must repopulate the panel at once.
watch(
  () => [activeFile.value?.path ?? "", activeFile.value?.language ?? ""] as const,
  () => scheduleLoad(true),
  { immediate: true },
);

// Typing must not issue one document-symbol request per keystroke.
watch(
  () => activeFile.value?.content ?? "",
  (next, previous) => {
    if (next !== previous) scheduleLoad(false);
  },
);

onBeforeUnmount(() => {
  if (debounceTimer) clearTimeout(debounceTimer);
});

function toggleBranch(id: string): void {
  const next = new Set(expandedIds.value);
  if (next.has(id)) next.delete(id);
  else next.add(id);
  expandedIds.value = next;
}

function isBranchExpanded(id: string): boolean {
  return Boolean(filterQuery.value.trim()) || expandedIds.value.has(id);
}

function collapseAll(): void {
  expandedIds.value = new Set();
}

/** Returns a usable 0-based start position, or null when the range is unusable. */
function safeStart(
  preferred: LSPRange | undefined,
  fallback: LSPRange | undefined,
): LSPPosition | null {
  const start = preferred?.start ?? fallback?.start;
  if (!start) return null;
  if (!Number.isFinite(start.line) || !Number.isFinite(start.character)) return null;
  return { line: Math.max(0, start.line), character: Math.max(0, start.character) };
}

function jumpToSymbol(symbol: LSPDocumentSymbol): void {
  // Falling back to `range` keeps a symbol clickable when the server omitted
  // `selectionRange`; reading it unguarded threw and broke the whole panel.
  const start = safeStart(symbol.selectionRange, symbol.range);
  if (!start) return;
  appState.cursorLine = start.line + 1;
  appState.cursorColumn = start.character + 1;
  appState.editorJumpSeq += 1;
}

function comparePosition(left: LSPPosition, right: LSPPosition): number {
  if (left.line !== right.line) return left.line - right.line;
  return left.character - right.character;
}

function rangeContains(range: LSPRange, position: LSPPosition): boolean {
  return comparePosition(range.start, position) <= 0 && comparePosition(position, range.end) <= 0;
}

const activeSymbolId = computed(() => {
  const position = {
    line: Math.max(0, appState.cursorLine - 1),
    character: Math.max(0, appState.cursorColumn - 1),
  };
  const { nodes } = flatIndex.value;
  let bestId = "";
  let bestDepth = -1;
  // Linear scan over the prebuilt index. This runs on every cursor move, so it
  // must not recurse: a click moves the cursor, and the old recursive walk paid
  // full tree traversal for each one.
  for (const node of nodes) {
    const range = node.symbol.range;
    if (!range?.start || !range.end) continue;
    if (!Number.isFinite(range.start.line) || !Number.isFinite(range.end.line)) continue;
    if (!rangeContains(range, position)) continue;
    if (node.depth > bestDepth) {
      bestId = node.id;
      bestDepth = node.depth;
    }
  }
  return bestId;
});

function symbolIcon(kind: number): Component {
  if ([1, 2, 3, 4].includes(kind)) return kind === 1 ? Document : Folder;
  if ([5, 10, 11, 19, 23].includes(kind)) return Grid;
  if ([6, 9, 12, 24, 25].includes(kind)) return Operation;
  if ([7, 8, 13, 14, 20, 22].includes(kind)) return Key;
  return DataAnalysis;
}
</script>

<template>
  <section class="outline-panel" :aria-label="t('outline.ariaLabel')">
    <header class="outline-panel__header">
      <span class="outline-panel__title">{{ t("outline.title") }}</span>
      <div class="outline-panel__actions">
        <button
          type="button"
          class="outline-panel__icon-button"
          :title="t('outline.collapseAll')"
          :aria-label="t('outline.collapseAll')"
          @click="collapseAll"
        >
          <el-icon><Fold /></el-icon>
        </button>
        <button
          type="button"
          class="outline-panel__icon-button"
          :class="{ 'outline-panel__icon-button--loading': loading }"
          :title="t('outline.refresh')"
          :aria-label="t('outline.refresh')"
          @click="loadSymbols"
        >
          <el-icon><RefreshRight /></el-icon>
        </button>
      </div>
    </header>

    <label v-if="activeFile" class="outline-panel__filter">
      <el-icon aria-hidden="true"><Search /></el-icon>
      <input
        v-model="filterQuery"
        data-test="outline-filter"
        type="search"
        :aria-label="t('outline.filterAria')"
        :placeholder="t('outline.filterPlaceholder')"
      />
    </label>

    <div class="outline-panel__body">
      <div v-if="loading" class="outline-panel__empty" data-test="outline-loading">
        {{ t("outline.loading") }}
      </div>
      <div v-else-if="!activeFile" class="outline-panel__empty" data-test="outline-empty">
        {{ t("outline.openFile") }}
      </div>
      <div v-else-if="visibleRows.length === 0" class="outline-panel__empty" data-test="outline-empty">
        {{ filterQuery.trim() ? t("outline.noMatches") : t("outline.noSymbols") }}
      </div>
      <ul v-else class="outline-panel__tree" :aria-label="t('outline.treeAria')">
        <li
          v-for="row in visibleRows"
          :key="row.id"
          class="outline-panel__row"
          :class="{ 'outline-panel__row--active': activeSymbolId === row.id }"
          :style="{ paddingLeft: `${6 + row.depth * 16}px` }"
        >
          <button
            v-if="row.hasChildren"
            type="button"
            class="outline-panel__toggle"
            :data-toggle="row.id"
            :aria-label="isBranchExpanded(row.id) ? t('outline.collapse') : t('outline.expand')"
            :aria-expanded="isBranchExpanded(row.id)"
            @click="toggleBranch(row.id)"
          >
            <el-icon>
              <ArrowDown v-if="isBranchExpanded(row.id)" />
              <ArrowRight v-else />
            </el-icon>
          </button>
          <span v-else class="outline-panel__toggle-placeholder" aria-hidden="true" />

          <el-icon class="outline-panel__kind" :data-kind="row.symbol.kind" aria-hidden="true">
            <component :is="symbolIcon(row.symbol.kind)" />
          </el-icon>
          <button
            type="button"
            class="outline-panel__symbol"
            :data-symbol="row.symbol.name"
            :data-depth="row.depth"
            :aria-current="activeSymbolId === row.id ? 'location' : undefined"
            :title="row.symbol.detail || row.symbol.name"
            @click="jumpToSymbol(row.symbol)"
          >
            <span class="outline-panel__name">{{ row.symbol.name }}</span>
            <span v-if="row.symbol.detail" class="outline-panel__detail">
              {{ row.symbol.detail }}
            </span>
          </button>
        </li>
      </ul>

      <!--
        GOAL-P0-08: a pathological document must degrade visibly, not silently.
        Truncating without saying so would let the user believe the outline is
        complete and conclude a symbol does not exist.
      -->
      <p v-if="outlineTruncated" class="outline-panel__truncated" data-test="outline-truncated" role="status">
        {{ t("outline.truncated") }}
      </p>
    </div>
  </section>
</template>

<style scoped>
.outline-panel {
  display: flex;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
  height: 100%;
  background: var(--color-sidebar-bg);
}

.outline-panel__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex: 0 0 32px;
  min-width: 0;
  padding: 0 6px 0 12px;
}

.outline-panel__title {
  overflow: hidden;
  color: var(--color-text-secondary);
  font-size: 11px;
  font-weight: 650;
  letter-spacing: 0;
  text-overflow: ellipsis;
  text-transform: uppercase;
  white-space: nowrap;
}

.outline-panel__actions {
  display: flex;
  align-items: center;
  gap: 2px;
}

.outline-panel__icon-button,
.outline-panel__toggle {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border: 0;
  background: transparent;
  color: var(--color-text-tertiary);
  cursor: pointer;
}

.outline-panel__icon-button {
  width: 26px;
  height: 26px;
  border-radius: var(--radius-sm);
}

.outline-panel__icon-button:hover,
.outline-panel__icon-button:focus-visible,
.outline-panel__toggle:hover,
.outline-panel__toggle:focus-visible {
  outline: none;
  background: var(--color-bg-surface-container-low);
  color: var(--color-text-primary);
}

.outline-panel__icon-button--loading .el-icon {
  animation: outline-spin 0.8s linear infinite;
}

.outline-panel__filter {
  display: flex;
  align-items: center;
  flex: 0 0 28px;
  gap: 6px;
  min-width: 0;
  margin: 0 8px 4px;
  padding: 0 7px;
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-sm);
  background: var(--color-bg-surface-container-lowest);
  color: var(--color-text-tertiary);
}

.outline-panel__filter:focus-within {
  border-color: var(--color-primary-focus);
}

.outline-panel__filter input {
  min-width: 0;
  width: 100%;
  border: 0;
  outline: 0;
  background: transparent;
  color: var(--color-text-primary);
  font: inherit;
  font-size: 12px;
  letter-spacing: 0;
}

.outline-panel__filter input::placeholder {
  color: var(--color-text-disabled);
}

.outline-panel__body {
  flex: 1;
  min-height: 0;
  overflow: auto;
}

.outline-panel__tree {
  min-width: max-content;
  margin: 0;
  padding: 0 4px 6px;
  list-style: none;
}

.outline-panel__row {
  display: flex;
  align-items: center;
  min-width: 0;
  height: 24px;
  padding-right: 4px;
  border-radius: var(--radius-xs);
  color: var(--color-text-secondary);
}

.outline-panel__row:hover {
  background: var(--color-bg-surface-container-low);
}

.outline-panel__row--active {
  background: color-mix(in srgb, var(--color-primary-focus) 14%, transparent);
  color: var(--color-text-primary);
}

.outline-panel__toggle,
.outline-panel__toggle-placeholder {
  flex: 0 0 20px;
  width: 20px;
  height: 20px;
  border-radius: var(--radius-xs);
}

.outline-panel__kind {
  flex: 0 0 15px;
  width: 15px;
  height: 15px;
  margin-right: 5px;
  color: var(--color-primary);
}

.outline-panel__symbol {
  display: flex;
  align-items: baseline;
  flex: 1;
  gap: 6px;
  min-width: 0;
  height: 22px;
  padding: 0;
  overflow: hidden;
  border: 0;
  outline: 0;
  background: transparent;
  color: inherit;
  font-family: var(--font-sans);
  font-size: 12px;
  letter-spacing: 0;
  text-align: left;
  cursor: pointer;
}

.outline-panel__name {
  flex: 0 0 auto;
  max-width: 220px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.outline-panel__detail {
  max-width: 180px;
  overflow: hidden;
  color: var(--color-text-disabled);
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.outline-panel__empty {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 64px;
  padding: 12px;
  color: var(--color-text-tertiary);
  font-size: 12px;
  letter-spacing: 0;
  text-align: center;
}

/*
 * GOAL-P0-08: the truncation notice must be visible rather than silent. A
 * degraded outline the user cannot see is indistinguishable from a broken one.
 */
.outline-panel__truncated {
  padding: 6px 12px;
  border-top: 1px solid var(--color-border, #333);
  color: var(--color-warning, #d29922);
  font-size: 11px;
  line-height: 1.4;
}

@keyframes outline-spin {
  to { transform: rotate(360deg); }
}

@media (prefers-reduced-motion: reduce) {
  .outline-panel__icon-button--loading .el-icon {
    animation: none;
  }
}
</style>
