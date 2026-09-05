<script setup lang="ts">
// Koyori IDE 组件 · Conversation Sidebar；交互服务：对话历史（ConversationService）。
// 喵，这是 Conversation Sidebar，负责 Koyori IDE 的界面呈现喵~
// Plan 11 Task 2 — Conversation sidebar with search, filters, context menu,
// favorites, tags, export-to-Markdown, and a trash view (soft-delete / restore).
// Uses existing conversationService bindings (list/save/delete); the backend
// Delete is now a soft-delete (sets deleted_at), and restore clears it via save.
import { ref, computed, onMounted, onUnmounted } from "vue";
import { useI18n } from "@/lib/i18n";
import { conversationService } from "@/api/services";
import type { Conversation } from "@/types";
import { notifyError, notifySuccess } from "@/lib/notifications";
import { ElMessageBox } from "element-plus";
import {
  getWindowOriginId,
  unwrapEventData,
  parseSyncOrigin,
} from "@/lib/windowOrigin";
import { subscribeCrossWindowEvent } from "@/lib/crossWindowSync";
import { aiState, isConversationTransitionBlocked } from "@/stores/ai";

const props = defineProps<{
  width: number;
  embedded?: boolean;
}>();

const emit = defineEmits<{
  (e: "select", id: string): void;
}>();

const { t } = useI18n();
const conversationBusy = computed(() => {
  void aiState.streaming;
  void aiState.globalStreamBusy;
  void aiState.activeStreamId;
  return isConversationTransitionBlocked();
});
const CONVERSATION_PAGE_SIZE = 50;
const conversations = ref<Conversation[]>([]);
const hasMoreConversations = ref(true);
const isLoadingConversations = ref(false);
const searchQuery = ref("");
const filterFavorite = ref(false);
const filterTag = ref("");
const showTrash = ref(false);
const contextMenu = ref<{ visible: boolean; x: number; y: number; conv: Conversation | null }>({
  visible: false,
  x: 0,
  y: 0,
  conv: null,
});

// All tags discovered across conversations (for the filter dropdown).
const allTags = computed(() => {
  const set = new Set<string>();
  for (const c of conversations.value) {
    for (const tag of c.tags ?? []) set.add(tag);
  }
  return Array.from(set).sort();
});

// Active (non-deleted) conversations.
const activeConversations = computed(() =>
  conversations.value.filter((c) => !c.deleted_at),
);

// Soft-deleted conversations (trash).
const trashConversations = computed(() =>
  conversations.value.filter((c) => c.deleted_at),
);

// Filtered list based on search + favorite + tag filters.
const filteredConversations = computed(() => {
  const q = searchQuery.value.trim().toLowerCase();
  return activeConversations.value.filter((c) => {
    if (filterFavorite.value && !c.favorite) return false;
    if (filterTag.value && !(c.tags ?? []).includes(filterTag.value)) return false;
    if (!q) return true;
    if (c.title.toLowerCase().includes(q)) return true;
    return (c.messages ?? []).some((m) => m.content.toLowerCase().includes(q));
  });
});

// Plan 11 Task 2 Step 6: drag-and-drop reordering state. dragIndex holds the
// source index of the in-flight drag; dragOverIndex holds the index under the
// cursor (for the drop indicator styling).
const dragIndex = ref<number | null>(null);
const dragOverIndex = ref<number | null>(null);

// displayedConversations applies manual sort_order when any conversation has a
// non-zero order. Manual-order items come first (ascending), unordered items
// fall back to created_at-desc. When no manual order exists, the filtered list
// is returned as-is (already created_at-desc from the backend List()).
const displayedConversations = computed(() => {
  const base = showTrash.value ? trashConversations.value : filteredConversations.value;
  const hasManualOrder = base.some((c) => (c.sort_order ?? 0) > 0);
  if (!hasManualOrder) return base;
  return [...base].sort((a, b) => {
    const ao = a.sort_order ?? 0;
    const bo = b.sort_order ?? 0;
    if (ao > 0 && bo > 0) return ao - bo;
    if (ao > 0) return -1;
    if (bo > 0) return 1;
    return b.created_at - a.created_at;
  });
});

function previewOf(c: Conversation): string {
  const last = (c.messages ?? []).slice(-1)[0];
  if (!last) return "";
  const text = last.content.trim();
  return text.length > 80 ? text.slice(0, 80) + "…" : text;
}

async function loadConversations(): Promise<void> {
  if (isLoadingConversations.value) return;
  isLoadingConversations.value = true;
  try {
    const list = await conversationService.list(0, CONVERSATION_PAGE_SIZE);
    conversations.value = Array.isArray(list) ? list : [];
    hasMoreConversations.value = conversations.value.length === CONVERSATION_PAGE_SIZE;
  } catch (e) {
    notifyError(t("aiAssistant.loadFailed", { error: String(e) }));
  } finally {
    isLoadingConversations.value = false;
  }
}

async function loadMoreConversations(): Promise<void> {
  if (isLoadingConversations.value || !hasMoreConversations.value) return;
  isLoadingConversations.value = true;
  try {
    const page = await conversationService.list(
      conversations.value.length,
      CONVERSATION_PAGE_SIZE,
    );
    const nextPage = Array.isArray(page) ? page : [];
    const seen = new Set(conversations.value.map((conversation) => conversation.id));
    conversations.value.push(
      ...nextPage.filter((conversation) => !seen.has(conversation.id)),
    );
    hasMoreConversations.value = nextPage.length === CONVERSATION_PAGE_SIZE;
  } catch (e) {
    notifyError(t("aiAssistant.loadFailed", { error: String(e) }));
  } finally {
    isLoadingConversations.value = false;
  }
}

function handleListScroll(event: Event): void {
  const target = event.currentTarget as HTMLElement;
  if (target.scrollHeight - target.scrollTop - target.clientHeight <= 48) {
    void loadMoreConversations();
  }
}

// prompt-6 Task 1: peer window saved a conversation → refresh list.
let conversationSavedCancel: (() => void) | null = null;
function onConversationSaved(event: unknown): void {
  const payload = unwrapEventData(event);
  const origin = parseSyncOrigin(payload);
  if (origin && origin === getWindowOriginId()) return;
  void loadConversations();
}

function handleSelect(id: string): void {
  if (showTrash.value || conversationBusy.value) return;
  emit("select", id);
}

function handleNew(): void {
  if (conversationBusy.value) return;
  emit("select", "");
}

function openContextMenu(e: MouseEvent | KeyboardEvent, conv: Conversation): void {
  e.preventDefault();
  const target = e.currentTarget as HTMLElement | null;
  const rect = target?.getBoundingClientRect();
  const isMouseEvent = e instanceof MouseEvent;
  contextMenu.value = {
    visible: true,
    x: isMouseEvent ? e.clientX : (rect?.left ?? 0) + 16,
    y: isMouseEvent ? e.clientY : (rect?.top ?? 0) + 16,
    conv,
  };
}

function onConversationKeydown(e: KeyboardEvent, conv: Conversation): void {
  if ((e.shiftKey && e.key === "F10") || e.key === "ContextMenu") {
    openContextMenu(e, conv);
  }
}

function closeContextMenu(): void {
  contextMenu.value.visible = false;
  contextMenu.value.conv = null;
}

function handleDocumentClick(): void {
  if (contextMenu.value.visible) closeContextMenu();
}

// Plan 11 Task 2 Step 6 — drag-and-drop reordering handlers.
function handleDragStart(_e: DragEvent, index: number): void {
  dragIndex.value = index;
}

function handleDragOver(e: DragEvent, index: number): void {
  e.preventDefault(); // allow drop
  dragOverIndex.value = index;
}

async function handleDrop(e: DragEvent, targetIndex: number): Promise<void> {
  e.preventDefault();
  const from = dragIndex.value;
  dragIndex.value = null;
  dragOverIndex.value = null;
  if (from === null || from === targetIndex) return;
  if (showTrash.value) return; // reordering is disabled in the trash view

  const list = [...displayedConversations.value];
  const [moved] = list.splice(from, 1);
  list.splice(targetIndex, 0, moved);
  // Reassign sort_order 1..N and persist only the changed ones.
  try {
    for (let i = 0; i < list.length; i++) {
      const newOrder = i + 1;
      if ((list[i].sort_order ?? 0) !== newOrder) {
        await conversationService.save({ ...list[i], sort_order: newOrder });
      }
    }
    await loadConversations();
  } catch (err) {
    notifyError(t("aiAssistant.saveFailed", { error: String(err) }));
  }
}

async function toggleFavorite(conv: Conversation): Promise<void> {
  closeContextMenu();
  const updated: Conversation = { ...conv, favorite: !conv.favorite };
  try {
    await conversationService.save(updated);
    await loadConversations();
  } catch (e) {
    notifyError(t("aiAssistant.saveFailed", { error: String(e) }));
  }
}

async function handleDelete(conv: Conversation): Promise<void> {
  closeContextMenu();
  try {
    await conversationService.delete(conv.id);
    notifySuccess(t("aiAssistant.movedToTrash"));
    await loadConversations();
  } catch (e) {
    notifyError(t("aiAssistant.deleteFailed", { error: String(e) }));
  }
}

async function handleRestore(conv: Conversation): Promise<void> {
  closeContextMenu();
  const restored: Conversation = { ...conv, deleted_at: 0 };
  try {
    await conversationService.save(restored);
    notifySuccess(t("aiAssistant.restored"));
    await loadConversations();
  } catch (e) {
    notifyError(t("aiAssistant.restoreFailed", { error: String(e) }));
  }
}

async function handleRename(conv: Conversation): Promise<void> {
  closeContextMenu();
  try {
    const { value } = await ElMessageBox.prompt(
      t("aiAssistant.renamePrompt"),
      t("aiAssistant.renameTitle"),
      {
        confirmButtonText: t("aiAssistant.rename"),
        cancelButtonText: t("common.cancel"),
        inputValue: conv.title,
        inputPattern: /.+/,
        inputErrorMessage: t("aiAssistant.renameErrorEmpty"),
      },
    );
    if (value) {
      await conversationService.save({ ...conv, title: value });
      await loadConversations();
    }
  } catch {
    // cancelled
  }
}

async function handleAddTag(conv: Conversation): Promise<void> {
  closeContextMenu();
  try {
    const { value } = await ElMessageBox.prompt(
      t("aiAssistant.addTagPrompt"),
      t("aiAssistant.addTagTitle"),
      {
        confirmButtonText: t("common.ok"),
        cancelButtonText: t("common.cancel"),
      },
    );
    const tag = value?.trim();
    if (!tag) return;
    const tags = Array.from(new Set([...(conv.tags ?? []), tag]));
    await conversationService.save({ ...conv, tags });
    await loadConversations();
  } catch {
    // cancelled
  }
}

async function handleMoveGroup(conv: Conversation): Promise<void> {
  closeContextMenu();
  try {
    const { value } = await ElMessageBox.prompt(
      t("aiAssistant.moveGroupPrompt"),
      t("aiAssistant.moveGroupTitle"),
      {
        confirmButtonText: t("common.ok"),
        cancelButtonText: t("common.cancel"),
        inputValue: conv.group ?? "",
      },
    );
    await conversationService.save({ ...conv, group: value?.trim() ?? "" });
    await loadConversations();
  } catch {
    // cancelled
  }
}

async function handleCopy(conv: Conversation): Promise<void> {
  closeContextMenu();
  const text = (conv.messages ?? [])
    .map((m) => `**${m.role}**: ${m.content}`)
    .join("\n\n");
  try {
    await navigator.clipboard.writeText(text);
    notifySuccess(t("aiAssistant.copied"));
  } catch (e) {
    notifyError(t("aiAssistant.copyFailed", { error: String(e) }));
  }
}

function handleExportMarkdown(conv: Conversation): void {
  closeContextMenu();
  const lines: string[] = [`# ${conv.title}`, ""];
  for (const m of conv.messages ?? []) {
    lines.push(`## ${m.role}`, "", m.content, "");
  }
  const md = lines.join("\n");
  const blob = new Blob([md], { type: "text/markdown;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `${conv.title || conv.id}.md`;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
}

function formatTime(ts: number): string {
  return new Date(ts * 1000).toLocaleDateString();
}

// Exposed for unit tests.
defineExpose({
  conversations,
  hasMoreConversations,
  isLoadingConversations,
  searchQuery,
  filterFavorite,
  filterTag,
  showTrash,
  filteredConversations,
  trashConversations,
  displayedConversations,
  dragIndex,
  dragOverIndex,
  loadConversations,
  loadMoreConversations,
  toggleFavorite,
  handleDelete,
  handleRestore,
  handleExportMarkdown,
  handleDragStart,
  handleDragOver,
  handleDrop,
  previewOf,
});

onMounted(() => {
  void loadConversations();
  document.addEventListener("click", handleDocumentClick);
  try {
    conversationSavedCancel = subscribeCrossWindowEvent("conversation:saved", onConversationSaved);
  } catch {
    /* Events unavailable in tests */
  }
});

onUnmounted(() => {
  document.removeEventListener("click", handleDocumentClick);
  if (conversationSavedCancel) {
    try {
      conversationSavedCancel();
    } catch {
      /* ignore */
    }
    conversationSavedCancel = null;
  }
});
</script>

<template>
  <aside
    class="ai-conv-sidebar"
    :class="{ 'ai-conv-sidebar--embedded': props.embedded }"
    :style="{ width: props.embedded ? '100%' : `${props.width}px` }"
  >
    <div class="ai-conv-sidebar__top">
      <button class="ai-conv-sidebar__new" :disabled="conversationBusy" @click="handleNew">
        + {{ t("aiAssistant.newConversation") }}
      </button>
      <input
        v-model="searchQuery"
        class="ai-conv-sidebar__search"
        type="text"
        :placeholder="t('aiAssistant.searchPlaceholder')"
      />
      <div class="ai-conv-sidebar__filters">
        <button
          class="ai-conv-sidebar__filter"
          :class="{ 'ai-conv-sidebar__filter--active': filterFavorite }"
          :aria-label="t('aiAssistant.filterFavorite')"
          :title="t('aiAssistant.filterFavorite')"
          @click="filterFavorite = !filterFavorite"
        >
          ★
        </button>
        <select
          v-if="allTags.length > 0"
          v-model="filterTag"
          class="ai-conv-sidebar__tag-select"
          :title="t('aiAssistant.filterTag')"
        >
          <option value="">{{ t("aiAssistant.allTags") }}</option>
          <option v-for="tag in allTags" :key="tag" :value="tag">{{ tag }}</option>
        </select>
      </div>
    </div>

    <div class="ai-conv-sidebar__list" @scroll.passive="handleListScroll">
      <div v-if="displayedConversations.length === 0" class="ai-conv-sidebar__empty">
        {{ showTrash ? t("aiAssistant.trashEmpty") : t("aiAssistant.noConversations") }}
      </div>
      <div
        v-for="(c, index) in displayedConversations"
        :key="c.id"
        class="ai-conv-sidebar__item"
        :class="{
          'ai-conv-sidebar__item--trash': showTrash,
          'ai-conv-sidebar__item--dragging': dragIndex === index,
          'ai-conv-sidebar__item--drag-over': dragOverIndex === index && dragIndex !== index,
        }"
        :draggable="!showTrash"
        role="button"
        :tabindex="conversationBusy ? -1 : 0"
        :aria-disabled="conversationBusy"
        :aria-label="c.title"
        @click="handleSelect(c.id)"
        @contextmenu="openContextMenu($event, c)"
        @keydown.enter.prevent="handleSelect(c.id)"
        @keydown.space.prevent="handleSelect(c.id)"
        @keydown="onConversationKeydown($event, c)"
        @dragstart="handleDragStart($event, index)"
        @dragover="handleDragOver($event, index)"
        @drop="handleDrop($event, index)"
        @dragend="dragIndex = null; dragOverIndex = null"
      >
        <div class="ai-conv-sidebar__item-head">
          <span class="ai-conv-sidebar__item-title">{{ c.title }}</span>
          <span
            v-if="c.favorite"
            class="ai-conv-sidebar__star"
            :title="t('aiAssistant.filterFavorite')"
          >★</span>
        </div>
        <div v-if="previewOf(c)" class="ai-conv-sidebar__item-preview">
          {{ previewOf(c) }}
        </div>
        <div class="ai-conv-sidebar__item-meta">
          <span class="ai-conv-sidebar__item-date">{{ formatTime(c.created_at) }}</span>
          <span v-if="c.group" class="ai-conv-sidebar__item-group">{{ c.group }}</span>
          <span v-if="c.mode" class="ai-conv-sidebar__item-mode">{{ c.mode }}</span>
        </div>
        <div v-if="(c.tags ?? []).length > 0" class="ai-conv-sidebar__item-tags">
          <span
            v-for="tag in (c.tags ?? []).slice(0, 4)"
            :key="tag"
            class="ai-conv-sidebar__tag-chip"
          >{{ tag }}</span>
        </div>
      </div>
    </div>

    <div class="ai-conv-sidebar__bottom">
      <button
        class="ai-conv-sidebar__trash-toggle"
        :class="{ 'ai-conv-sidebar__trash-toggle--active': showTrash }"
        @click="showTrash = !showTrash"
      >
        {{ t("aiAssistant.trash") }} ({{ trashConversations.length }})
      </button>
      <p v-if="showTrash" class="ai-conv-sidebar__trash-hint">
        {{ t("aiAssistant.trashHint") }}
      </p>
    </div>

    <!-- Context menu -->
    <ul
      v-if="contextMenu.visible && contextMenu.conv"
      class="ai-conv-sidebar__ctx"
      role="menu"
      :aria-label="t('aiAssistant.conversationActions')"
      :style="{ left: `${contextMenu.x}px`, top: `${contextMenu.y}px` }"
    >
      <li v-if="!showTrash" role="menuitem" tabindex="0" @click="contextMenu.conv && handleRename(contextMenu.conv)" @keydown.enter.prevent="contextMenu.conv && handleRename(contextMenu.conv)" @keydown.space.prevent="contextMenu.conv && handleRename(contextMenu.conv)">
        {{ t("aiAssistant.rename") }}
      </li>
      <li v-if="!showTrash" role="menuitem" tabindex="0" @click="contextMenu.conv && toggleFavorite(contextMenu.conv)" @keydown.enter.prevent="contextMenu.conv && toggleFavorite(contextMenu.conv)" @keydown.space.prevent="contextMenu.conv && toggleFavorite(contextMenu.conv)">
        {{ t("aiAssistant.toggleFavorite") }}
      </li>
      <li v-if="!showTrash" role="menuitem" tabindex="0" @click="contextMenu.conv && handleAddTag(contextMenu.conv)" @keydown.enter.prevent="contextMenu.conv && handleAddTag(contextMenu.conv)" @keydown.space.prevent="contextMenu.conv && handleAddTag(contextMenu.conv)">
        {{ t("aiAssistant.addTag") }}
      </li>
      <li v-if="!showTrash" role="menuitem" tabindex="0" @click="contextMenu.conv && handleMoveGroup(contextMenu.conv)" @keydown.enter.prevent="contextMenu.conv && handleMoveGroup(contextMenu.conv)" @keydown.space.prevent="contextMenu.conv && handleMoveGroup(contextMenu.conv)">
        {{ t("aiAssistant.moveGroup") }}
      </li>
      <li role="menuitem" tabindex="0" @click="contextMenu.conv && handleCopy(contextMenu.conv)" @keydown.enter.prevent="contextMenu.conv && handleCopy(contextMenu.conv)" @keydown.space.prevent="contextMenu.conv && handleCopy(contextMenu.conv)">
        {{ t("aiAssistant.copy") }}
      </li>
      <li role="menuitem" tabindex="0" @click="contextMenu.conv && handleExportMarkdown(contextMenu.conv)" @keydown.enter.prevent="contextMenu.conv && handleExportMarkdown(contextMenu.conv)" @keydown.space.prevent="contextMenu.conv && handleExportMarkdown(contextMenu.conv)">
        {{ t("aiAssistant.exportMarkdown") }}
      </li>
      <li v-if="!showTrash" class="ai-conv-sidebar__ctx--danger" role="menuitem" tabindex="0" @click="contextMenu.conv && handleDelete(contextMenu.conv)" @keydown.enter.prevent="contextMenu.conv && handleDelete(contextMenu.conv)" @keydown.space.prevent="contextMenu.conv && handleDelete(contextMenu.conv)">
        {{ t("aiAssistant.moveToTrash") }}
      </li>
      <li v-if="showTrash" role="menuitem" tabindex="0" @click="contextMenu.conv && handleRestore(contextMenu.conv)" @keydown.enter.prevent="contextMenu.conv && handleRestore(contextMenu.conv)" @keydown.space.prevent="contextMenu.conv && handleRestore(contextMenu.conv)">
        {{ t("aiAssistant.restore") }}
      </li>
    </ul>
  </aside>
</template>

<style scoped>
.ai-conv-sidebar {
  display: flex;
  flex-direction: column;
  border-right: 1px solid var(--color-border-subtle, #2a2a2a);
  background: var(--color-bg-surface, #fafafc);
  flex-shrink: 0;
  overflow: hidden;
  position: relative;
}
.ai-conv-sidebar--embedded {
  height: 100%;
  border-right: 0;
  background: transparent;
}
.ai-conv-sidebar__top {
  padding: 8px;
  border-bottom: 1px solid var(--color-border-subtle, #2a2a2a);
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.ai-conv-sidebar__new {
  width: 100%;
  padding: 6px;
  font-size: 12px;
  border: 1px dashed var(--color-border-default, #3a3a3a);
  border-radius: 6px;
  background: transparent;
  color: var(--color-text-secondary, #aaa);
  cursor: pointer;
}
.ai-conv-sidebar__new:hover {
  border-color: var(--color-accent, #3b82f6);
  color: var(--color-text-primary, #e0e0e0);
}
.ai-conv-sidebar__search {
  width: 100%;
  padding: 5px 8px;
  font-size: 12px;
  background: var(--color-bg-elevated, #f5f5f7);
  color: var(--color-text-primary, #e0e0e0);
  border: 1px solid var(--color-border-default, #3a3a3a);
  border-radius: 4px;
  box-sizing: border-box;
}
.ai-conv-sidebar__filters {
  display: flex;
  gap: 6px;
  align-items: center;
}
.ai-conv-sidebar__filter {
  padding: 3px 8px;
  font-size: 13px;
  border: 1px solid var(--color-border-default, #3a3a3a);
  border-radius: 4px;
  background: transparent;
  color: var(--color-text-secondary, #aaa);
  cursor: pointer;
}
.ai-conv-sidebar__filter--active {
  background: var(--color-accent, #3b82f6);
  color: #fff;
  border-color: var(--color-accent, #3b82f6);
}
.ai-conv-sidebar__tag-select {
  flex: 1;
  padding: 3px;
  font-size: 11px;
  background: var(--color-bg-elevated, #f5f5f7);
  color: var(--color-text-primary, #e0e0e0);
  border: 1px solid var(--color-border-default, #3a3a3a);
  border-radius: 4px;
}
.ai-conv-sidebar__list {
  flex: 1;
  overflow-y: auto;
  padding: 4px;
}
.ai-conv-sidebar__empty {
  padding: 12px;
  font-size: 12px;
  color: var(--color-text-secondary, #888);
  text-align: center;
}
.ai-conv-sidebar__item {
  padding: 8px;
  margin-bottom: 4px;
  border-radius: 6px;
  cursor: pointer;
  transition: background 0.1s;
}
.ai-conv-sidebar__item:hover {
  background: var(--color-bg-elevated, #f5f5f7);
}
.ai-conv-sidebar__item:focus-visible,
.ai-conv-sidebar__ctx li:focus-visible {
  outline: 2px solid var(--color-primary-focus);
  outline-offset: -2px;
}
.ai-conv-sidebar__item--trash {
  opacity: 0.7;
}
.ai-conv-sidebar__item--dragging {
  opacity: 0.4;
}
.ai-conv-sidebar__item--drag-over {
  border-top: 2px solid var(--color-accent, #3b82f6);
}
.ai-conv-sidebar__item-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.ai-conv-sidebar__item-title {
  font-size: 12px;
  font-weight: 500;
  color: var(--color-text-primary, #e0e0e0);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1;
}
.ai-conv-sidebar__star {
  font-size: 12px;
  color: var(--color-warning, #f0a020);
  flex-shrink: 0;
}
.ai-conv-sidebar__item-preview {
  font-size: 11px;
  color: var(--color-text-secondary, #888);
  margin-top: 3px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ai-conv-sidebar__item-meta {
  display: flex;
  gap: 6px;
  margin-top: 4px;
  font-size: 10px;
  color: var(--color-text-secondary, #666);
}
.ai-conv-sidebar__item-group,
.ai-conv-sidebar__item-mode {
  padding: 1px 5px;
  background: var(--color-bg-elevated, #f5f5f7);
  border-radius: 3px;
}
.ai-conv-sidebar__item-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 3px;
  margin-top: 4px;
}
.ai-conv-sidebar__tag-chip {
  font-size: 10px;
  padding: 1px 5px;
  background: var(--color-accent-soft, rgba(59, 130, 246, 0.15));
  color: var(--color-accent, #3b82f6);
  border-radius: 3px;
}
.ai-conv-sidebar__bottom {
  padding: 6px 8px;
  border-top: 1px solid var(--color-border-subtle, #2a2a2a);
}
.ai-conv-sidebar__trash-toggle {
  width: 100%;
  padding: 4px;
  font-size: 11px;
  border: none;
  border-radius: 4px;
  background: transparent;
  color: var(--color-text-secondary, #aaa);
  cursor: pointer;
}
.ai-conv-sidebar__trash-toggle--active {
  background: var(--color-bg-elevated, #f5f5f7);
  color: var(--color-text-primary, #e0e0e0);
}
.ai-conv-sidebar__trash-hint {
  margin: 4px 0 0;
  font-size: 10px;
  line-height: 1.4;
  color: var(--color-text-secondary, #666);
  text-align: center;
}
.ai-conv-sidebar__ctx {
  position: fixed;
  list-style: none;
  margin: 0;
  padding: 4px 0;
  background: var(--color-bg-elevated, #f5f5f7);
  border: 1px solid var(--color-border-default, #3a3a3a);
  border-radius: 6px;
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.4);
  z-index: 1000;
  min-width: 140px;
}
.ai-conv-sidebar__ctx li {
  padding: 6px 14px;
  font-size: 12px;
  color: var(--color-text-primary, #e0e0e0);
  cursor: pointer;
}
.ai-conv-sidebar__ctx li:hover {
  background: var(--color-accent, #3b82f6);
  color: #fff;
}
.ai-conv-sidebar__ctx--danger {
  color: var(--color-danger, #ef4444) !important;
}
.ai-conv-sidebar__ctx--danger:hover {
  background: var(--color-danger, #ef4444) !important;
  color: #fff !important;
}
</style>
