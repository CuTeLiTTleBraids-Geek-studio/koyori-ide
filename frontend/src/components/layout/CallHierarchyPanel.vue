<script setup lang="ts">
// Koyori IDE 组件 · Call Hierarchy Panel。
// 喵，这是 Call Hierarchy Panel，负责 Koyori IDE 的界面呈现喵~
import { computed, ref, watch } from "vue";
import { ArrowRight, Loading, Refresh, Switch } from "@element-plus/icons-vue";
import { ElMessage } from "element-plus";
import { requestEditorJump } from "@/stores/app";
import { openFileFromPath } from "@/stores/editor";
import { layoutState } from "@/stores/layout";
import {
  callHierarchyQuery,
  getLSPCallHierarchyIncomingCalls,
  getLSPCallHierarchyOutgoingCalls,
  getLSPTypeHierarchySubtypes,
  getLSPTypeHierarchySupertypes,
  prepareLSPCallHierarchy,
  prepareLSPTypeHierarchy,
  type CallHierarchyQuery,
} from "@/stores/lsp";
import { useI18n } from "@/lib/i18n";
import type { LSPCallHierarchyItem, LSPTypeHierarchyItem } from "@/types";

type Mode = "call" | "type";
type Direction = "incoming" | "outgoing" | "supertypes" | "subtypes";
type HierarchyItem = LSPCallHierarchyItem | LSPTypeHierarchyItem;

interface HierarchyNode {
  id: string;
  item: HierarchyItem;
  children: HierarchyNode[] | null;
  expanded: boolean;
  loading: boolean;
}

interface VisibleNode {
  node: HierarchyNode;
  depth: number;
}

const { t } = useI18n();

const mode = ref<Mode>("call");
const direction = ref<Direction>("incoming");
const loading = ref(false);
const rootItems = ref<HierarchyItem[]>([]);
const rootNodes = ref<HierarchyNode[]>([]);
let queryGeneration = 0;

const hasRoot = computed(() => rootNodes.value.length > 0);
const title = computed(() =>
  mode.value === "call"
    ? direction.value === "incoming"
      ? t("callHierarchy.incomingTitle")
      : t("callHierarchy.outgoingTitle")
    : direction.value === "supertypes"
      ? t("callHierarchy.supertypesTitle")
      : t("callHierarchy.subtypesTitle"),
);

const visibleNodes = computed<VisibleNode[]>(() => {
  const flattened: VisibleNode[] = [];
  const append = (nodes: HierarchyNode[], depth: number) => {
    for (const node of nodes) {
      flattened.push({ node, depth });
      if (node.expanded && node.children) append(node.children, depth + 1);
    }
  };
  append(rootNodes.value, 0);
  return flattened;
});

function nodeId(item: HierarchyItem, parentId: string, index: number): string {
  return `${parentId}:${item.filePath}:${item.line}:${item.column}:${item.name}:${index}`;
}

function makeNodes(items: HierarchyItem[], parentId = "root"): HierarchyNode[] {
  return items.map((item, index) => ({
    id: nodeId(item, parentId, index),
    item,
    children: null,
    expanded: false,
    loading: false,
  }));
}

watch(
  () => callHierarchyQuery.value,
  async (query: CallHierarchyQuery | null) => {
    if (!query) return;
    mode.value = query.mode;
    direction.value = query.mode === "call" ? "incoming" : "supertypes";
    await runQuery(query);
  },
  { immediate: true },
);

async function runQuery(query: CallHierarchyQuery) {
  const generation = ++queryGeneration;
  loading.value = true;
  rootItems.value = [];
  rootNodes.value = [];
  try {
    const items = query.mode === "call"
      ? await prepareLSPCallHierarchy(query.language, query.filePath, query.line, query.column, query.content)
      : await prepareLSPTypeHierarchy(query.language, query.filePath, query.line, query.column, query.content);
    if (generation !== queryGeneration) return;
    rootItems.value = items;
    rootNodes.value = makeNodes(items);
    if (items.length === 0) ElMessage.info(t("callHierarchy.noSymbol"));
  } catch (error) {
    if (generation === queryGeneration) {
      ElMessage.warning(error instanceof Error ? error.message : String(error));
    }
  } finally {
    if (generation === queryGeneration) loading.value = false;
  }
}

async function fetchChildren(node: HierarchyNode): Promise<HierarchyItem[]> {
  const query = callHierarchyQuery.value;
  if (!query) return [];

  if (mode.value === "call") {
    const item = node.item as LSPCallHierarchyItem;
    if (direction.value === "incoming") {
      const calls = await getLSPCallHierarchyIncomingCalls(
        query.language,
        query.filePath,
        query.content,
        item,
      );
      return calls.map((call) => call.from);
    }
    const calls = await getLSPCallHierarchyOutgoingCalls(
      query.language,
      query.filePath,
      query.content,
      item,
    );
    return calls.map((call) => call.to);
  }

  const item = node.item as LSPTypeHierarchyItem;
  return direction.value === "supertypes"
    ? getLSPTypeHierarchySupertypes(query.language, query.filePath, query.content, item)
    : getLSPTypeHierarchySubtypes(query.language, query.filePath, query.content, item);
}

async function toggleNode(node: HierarchyNode) {
  if (node.expanded) {
    node.expanded = false;
    return;
  }

  node.expanded = true;
  if (node.children !== null || node.loading) return;
  node.loading = true;
  try {
    node.children = makeNodes(await fetchChildren(node), node.id);
  } catch (error) {
    node.expanded = false;
    ElMessage.warning(error instanceof Error ? error.message : String(error));
  } finally {
    node.loading = false;
  }
}

async function jumpToItem(item: HierarchyItem) {
  await openFileFromPath(item.filePath);
  requestEditorJump(
    item.filePath,
    item.selectionLine + 1,
    item.selectionColumn + 1,
    layoutState.tree.activeLeafId,
  );
}

function switchDirection() {
  direction.value = mode.value === "call"
    ? direction.value === "incoming" ? "outgoing" : "incoming"
    : direction.value === "supertypes" ? "subtypes" : "supertypes";
  rootNodes.value = makeNodes(rootItems.value);
}

function refresh() {
  if (callHierarchyQuery.value) void runQuery(callHierarchyQuery.value);
}
</script>

<template>
  <section class="call-hierarchy-panel" :aria-label="title">
    <header class="chp__header">
      <h3 class="chp__title">{{ title }}</h3>
      <el-button-group>
        <el-button
          :icon="Switch"
          size="small"
          :title="t('callHierarchy.switchDirection')"
          :aria-label="t('callHierarchy.switchDirection')"
          @click="switchDirection"
        />
        <el-button
          :icon="Refresh"
          size="small"
          :title="t('callHierarchy.refresh')"
          :aria-label="t('callHierarchy.refresh')"
          :loading="loading"
          @click="refresh"
        />
      </el-button-group>
    </header>

    <div
      v-if="!hasRoot && !loading"
      class="chp__empty"
      role="status"
      aria-live="polite"
    >
      {{ t("callHierarchy.empty") }}
    </div>

    <div
      v-else
      class="chp__tree"
      role="tree"
      :aria-label="title"
      :aria-busy="loading"
      aria-live="polite"
    >
      <template v-for="entry in visibleNodes" :key="entry.node.id">
        <button
          type="button"
          class="chp__item"
          role="treeitem"
          :aria-level="entry.depth + 1"
          :aria-expanded="entry.node.expanded"
          :style="{ paddingLeft: `${8 + entry.depth * 18}px` }"
          @click="toggleNode(entry.node)"
          @dblclick.stop="jumpToItem(entry.node.item)"
        >
          <el-icon
            class="chp__chevron"
            :class="{ 'chp__chevron--expanded': entry.node.expanded, 'is-loading': entry.node.loading }"
            aria-hidden="true"
          >
            <Loading v-if="entry.node.loading" />
            <ArrowRight v-else />
          </el-icon>
          <span class="chp__body">
            <span class="chp__row">
              <span class="chp__name">{{ entry.node.item.name }}</span>
              <span v-if="entry.node.item.detail" class="chp__detail">{{ entry.node.item.detail }}</span>
            </span>
            <span class="chp__path">{{ entry.node.item.filePath }}:{{ entry.node.item.line + 1 }}</span>
          </span>
        </button>
        <div
          v-if="entry.node.expanded && entry.node.children?.length === 0"
          class="chp__no-children"
          role="status"
          :style="{ paddingLeft: `${32 + entry.depth * 18}px` }"
        >
          {{ t("callHierarchy.noChildren") }}
        </div>
      </template>
    </div>
  </section>
</template>

<style scoped>
.call-hierarchy-panel {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  padding: 8px;
  gap: 8px;
}

.chp__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.chp__title {
  min-width: 0;
  margin: 0;
  overflow: hidden;
  color: var(--color-text-primary);
  font-size: 13px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.chp__empty {
  padding: 16px 8px;
  color: var(--color-text-secondary);
  font-size: 12px;
  text-align: center;
}

.chp__tree {
  flex: 1;
  min-height: 0;
  overflow: auto;
}

.chp__item {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  min-height: 42px;
  padding-top: 5px;
  padding-right: 6px;
  padding-bottom: 5px;
  border: 0;
  border-radius: var(--radius-sm);
  color: var(--color-text-primary);
  background: transparent;
  cursor: pointer;
  text-align: left;
}

.chp__item:hover,
.chp__item:focus-visible {
  background: var(--color-bg-surface-container-low);
}

.chp__chevron {
  flex: 0 0 14px;
  transition: transform var(--transition-fast);
}

.chp__chevron--expanded:not(.is-loading) {
  transform: rotate(90deg);
}

.chp__body {
  min-width: 0;
  flex: 1;
}

.chp__row {
  display: flex;
  align-items: baseline;
  gap: 6px;
  min-width: 0;
}

.chp__name {
  overflow: hidden;
  font-size: 12px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.chp__detail,
.chp__path {
  overflow: hidden;
  color: var(--color-text-tertiary);
  font-size: 10px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.chp__path {
  display: block;
  margin-top: 2px;
  font-family: var(--font-mono);
}

.chp__no-children {
  min-height: 28px;
  padding-top: 6px;
  padding-right: 6px;
  padding-bottom: 6px;
  color: var(--color-text-tertiary);
  font-size: 11px;
}

@media (prefers-reduced-motion: reduce) {
  .chp__chevron {
    transition: none;
  }
}
</style>
