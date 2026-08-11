<script setup lang="ts">
// Koyori IDE 组件 · Tab Bar。
// 喵，这是 Tab Bar，负责 Koyori IDE 的界面呈现喵~
import { computed, nextTick } from "vue";
import { Close } from "@element-plus/icons-vue";
import { editorState } from "@/stores/editor";
import { appState, ensureEditorGroup, moveFileBetweenEditorGroups } from "@/stores/app";
import { useI18n } from "@/lib/i18n";

const props = withDefaults(defineProps<{
  groupId?: string;
  groupActive?: boolean;
}>(), {
  groupId: "primary-editor-group",
  groupActive: true,
});

const emit = defineEmits<{
  (e: "select", path: string): void;
  (e: "close", path: string): void;
}>();

const { t } = useI18n();

ensureEditorGroup(
  props.groupId,
  editorState.openFiles.map((file) => file.path),
  editorState.activeFilePath,
);

const files = computed(() => {
  const paths = appState.editorGroupFilePaths[props.groupId] ?? [];
  const byPath = new Map(editorState.openFiles.map((file) => [file.path, file]));
  return paths.flatMap((path) => {
    const file = byPath.get(path);
    return file ? [file] : [];
  });
});

const activePath = computed(() => appState.editorGroupActiveFiles[props.groupId] ?? null);

function handleSelect(path: string) {
  emit("select", path);
}

function handleClose(path: string) {
  emit("close", path);
}

function handleTabKeydown(event: KeyboardEvent, index: number) {
  let targetIndex = index;
  if (event.key === "ArrowLeft") targetIndex = (index - 1 + files.value.length) % files.value.length;
  else if (event.key === "ArrowRight") targetIndex = (index + 1) % files.value.length;
  else if (event.key === "Home") targetIndex = 0;
  else if (event.key === "End") targetIndex = files.value.length - 1;
  else return;

  event.preventDefault();
  const target = files.value[targetIndex];
  if (!target) return;
  handleSelect(target.path);
  void nextTick(() => {
    const tablist = (event.currentTarget as HTMLElement | null)?.closest('[role="tablist"]');
    const tabs = tablist?.querySelectorAll<HTMLElement>('[role="tab"]');
    tabs?.[targetIndex]?.focus();
  });
}

interface DraggedTab {
  path: string;
  sourceGroupId: string;
}

const TAB_DRAG_TYPE = "application/x-koyori-ide-editor-tab";

function handleDragStart(event: DragEvent, path: string) {
  if (!event.dataTransfer) return;
  const payload: DraggedTab = { path, sourceGroupId: props.groupId };
  event.dataTransfer.effectAllowed = "move";
  event.dataTransfer.setData(TAB_DRAG_TYPE, JSON.stringify(payload));
  event.dataTransfer.setData("text/plain", path);
}

function readDraggedTab(event: DragEvent): DraggedTab | null {
  const raw = event.dataTransfer?.getData(TAB_DRAG_TYPE);
  if (!raw) return null;
  try {
    const value = JSON.parse(raw) as Partial<DraggedTab>;
    return typeof value.path === "string" && typeof value.sourceGroupId === "string"
      ? value as DraggedTab
      : null;
  } catch {
    return null;
  }
}

function handleDrop(event: DragEvent, targetIndex = files.value.length) {
  event.preventDefault();
  const dragged = readDraggedTab(event);
  if (!dragged) return;
  moveFileBetweenEditorGroups(
    dragged.path,
    dragged.sourceGroupId,
    props.groupId,
    targetIndex,
  );
  emit("select", dragged.path);
}
</script>

<template>
  <div
    class="tab-bar"
    role="tablist"
    :aria-label="t('layout.editorView')"
    :data-editor-group="groupId"
    @dragover.prevent
    @drop.stop="handleDrop"
  >
    <div
      v-for="(file, index) in files"
      :key="file.path"
      class="tab-bar__item"
      :class="{ 'tab-bar__item--active': file.path === activePath }"
    >
      <button
        type="button"
        class="tab-bar__tab tab-bar__select"
        :class="{ 'tab-bar__tab--active': file.path === activePath }"
        role="tab"
        draggable="true"
        :tabindex="file.path === activePath ? 0 : -1"
        :aria-selected="file.path === activePath"
        :aria-label="file.name"
        @click="handleSelect(file.path)"
        @dragstart="handleDragStart($event, file.path)"
        @dragover.prevent
        @drop.stop="handleDrop($event, index)"
        @keydown.enter.prevent="handleSelect(file.path)"
        @keydown.space.prevent="handleSelect(file.path)"
        @keydown="handleTabKeydown($event, index)"
      >
        <span class="tab-bar__name">{{ file.name }}</span>
        <span v-if="file.isDirty" class="tab-bar__dirty" aria-hidden="true" />
      </button>
      <button
        type="button"
        class="tab-bar__close"
        :aria-label="t('tabBar.closeTabAria')"
        @click.stop="handleClose(file.path)"
      >
        <el-icon :size="12"><Close /></el-icon>
      </button>
    </div>
  </div>
</template>

<style scoped>
.tab-bar {
  display: flex;
  align-items: center;
  flex: 0 0 36px;
  height: 36px;
  min-height: 36px;
  padding: 0 8px;
  overflow-x: auto;
  gap: 2px;
  border-bottom: 1px solid var(--color-border-subtle);
  background: var(--color-bg-surface-dim);
}

.tab-bar__item {
  display: flex;
  align-items: center;
  gap: 6px;
  height: 28px;
  padding: 0 6px 0 12px;
  border-radius: var(--radius-sm);
  color: var(--color-text-tertiary);
  background: transparent;
  font-size: 12px;
  white-space: nowrap;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}

.tab-bar__item:hover {
  color: var(--color-text-secondary);
  background: var(--color-bg-surface-container-low);
}

.tab-bar__item--active,
.tab-bar__item--active:hover {
  color: var(--color-text-primary);
  background: var(--color-bg-base);
}

.tab-bar__select {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  height: 100%;
  padding: 0;
  border: 0;
  color: inherit;
  background: transparent;
  font: inherit;
  cursor: pointer;
}

.tab-bar__select:focus-visible {
  outline: var(--focus-ring-width, 2px) solid var(--focus-ring-color, var(--color-primary));
  outline-offset: -2px;
}

.tab-bar__name {
  font-weight: 400;
}

.tab-bar__tab--active .tab-bar__name {
  font-weight: 500;
}

.tab-bar__dirty {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--color-primary);
}

.tab-bar__close {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  border: none;
  border-radius: var(--radius-xs);
  color: var(--color-text-tertiary);
  background: transparent;
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}

.tab-bar__close:hover {
  color: var(--color-text-primary);
  background-color: color-mix(in srgb, var(--color-text-primary) 12%, transparent);
}

@media (prefers-reduced-motion: reduce) {
  .tab-bar__item,
  .tab-bar__close {
    transition: none;
  }
}
</style>
