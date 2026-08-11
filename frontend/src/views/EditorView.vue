<script setup lang="ts">
// Koyori IDE 组件 · Editor View。
// 喵，这是 Editor View，负责 Koyori IDE 的界面呈现喵~
import { computed, ref, watch, onMounted, onBeforeUnmount } from "vue";
import CodeEditor from "@/components/editor/CodeEditor.vue";
import TabBar from "@/components/editor/TabBar.vue";
import BreadcrumbBar from "@/components/layout/BreadcrumbBar.vue";
import MarkdownContent from "@/components/common/MarkdownContent.vue";
import ModalOverlay from "@/components/common/ModalOverlay.vue";
import { Document } from "@element-plus/icons-vue";
import {
  activateEditorGroupFile,
  addFileToEditorGroup,
  appState,
  editorGroupsContainingFile,
  ensureEditorGroup,
  moveFileBetweenEditorGroups,
  removeFileFromEditorGroup,
} from "@/stores/app";
import {
  editorState,
  closeFile,
  updateContent,
  setupAutoSave,
  saveOnFocusChange,
  openFileFromPath,
  saveFilePath,
  dismissSaveConflict,
  resolveSaveConflict,
  saveConflictState,
} from "@/stores/editor";
import { renderMarkdown } from "@/lib/markdown";
import { notifyError, notifySuccess } from "@/lib/notifications";
import { useI18n } from "@/lib/i18n";

// N-116: Wails webview File objects expose the real filesystem path.
// Define a typed interface instead of using `as any` casts.
interface WailsFile extends File {
  path?: string;
  filePath?: string;
}

interface DraggedTab {
  path: string;
  sourceGroupId: string;
}

const TAB_DRAG_TYPE = "application/x-koyori-ide-editor-tab";

const props = withDefaults(defineProps<{
  groupId?: string;
  active?: boolean;
}>(), {
  groupId: "primary-editor-group",
  active: true,
});

const { t } = useI18n();

const cursorLine = ref(1);
const cursorColumn = ref(1);
const showPreview = ref(false);
const isDragOver = ref(false);

// 文件切换时的淡入动画触发器。监听 activeFile.path 变化，短暂挂载
// is-file-switching 类触发 CSS keyframe 动画。不强制 CodeEditor 重新
// 挂载（Monaco 重建代价高），仅对编辑器外层做透明度脉冲。
const isFileSwitching = ref(false);
let switchTimer: number | undefined;
// M-14: 存储 setupAutoSave 返回的 disposer，在卸载时调用以清除定时器。
let stopAutoSave: (() => void) | null = null;

ensureEditorGroup(
  props.groupId,
  editorState.openFiles.map((file) => file.path),
  editorState.activeFilePath,
);

const groupPaths = computed(() => appState.editorGroupFilePaths[props.groupId] ?? []);
const groupFiles = computed(() => {
  const byPath = new Map(editorState.openFiles.map((file) => [file.path, file]));
  return groupPaths.value.flatMap((path) => {
    const file = byPath.get(path);
    return file ? [file] : [];
  });
});
const groupActivePath = computed(() => appState.editorGroupActiveFiles[props.groupId] ?? null);
const groupActiveFile = computed(() =>
  editorState.openFiles.find((file) => file.path === groupActivePath.value) ?? null,
);
const hasOpenFiles = computed(() => groupFiles.value.length > 0);
const activeContent = computed(() => groupActiveFile.value?.content ?? "");

function syncActiveGroupPath(path = groupActivePath.value) {
  if (!props.active) return;
  editorState.activeFilePath = path;
  appState.currentFilePath = path;
}

const isMarkdown = computed(() => {
  const path = groupActiveFile.value?.path ?? "";
  return /\.(md|markdown|mdown)$/i.test(path);
});

const previewHtml = computed(() => {
  const content = groupActiveFile.value?.content ?? "";
  return renderMarkdown(content);
});

function handleTabSelect(path: string) {
  activateEditorGroupFile(props.groupId, path);
  syncActiveGroupPath(path);
}

async function handleTabClose(path: string) {
  removeFileFromEditorGroup(props.groupId, path);
  if (editorGroupsContainingFile(path).length === 0) closeFile(path);
  syncActiveGroupPath();
}

function handleContentChange(value: string) {
  if (groupActivePath.value) {
    updateContent(groupActivePath.value, value);
  }
}

function handleCursorChange(line: number, column: number) {
  cursorLine.value = line;
  cursorColumn.value = column;
  appState.cursorLine = line;
  appState.cursorColumn = column;
}

async function handleSave() {
  if (!groupActivePath.value) return;
  await saveFilePath(groupActivePath.value);
}

async function handleSaveConflict(action: "overwrite" | "reload") {
  await resolveSaveConflict(action);
}

function handleBlur() {
  if (!props.active) return;
  saveOnFocusChange(() => appState.autoSave);
}

function handleKeydown(e: KeyboardEvent) {
  if ((e.ctrlKey || e.metaKey) && e.key === "s") {
    e.preventDefault();
    handleSave();
  }
}

function readDraggedTab(e: DragEvent): DraggedTab | null {
  const raw = e.dataTransfer?.getData(TAB_DRAG_TYPE);
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

// Drag & drop accepts both editor tabs and native files. Wails exposes the
// webview's FileList with real paths on `file.path` for native file drags.
function handleDragOver(e: DragEvent) {
  if (!e.dataTransfer) return;
  if (e.dataTransfer.types.includes(TAB_DRAG_TYPE)) {
    e.preventDefault();
    e.dataTransfer.dropEffect = "move";
    isDragOver.value = false;
    return;
  }
  if (e.dataTransfer.types.includes("Files")) {
    e.preventDefault();
    e.dataTransfer.dropEffect = "copy";
    isDragOver.value = true;
  }
}

function handleDragLeave(e: DragEvent) {
  // Only clear when leaving the editor-view root, not when entering children.
  const related = e.relatedTarget as Node | null;
  const root = (e.currentTarget as HTMLElement);
  if (!related || !root.contains(related)) {
    isDragOver.value = false;
  }
}

async function handleDrop(e: DragEvent) {
  e.preventDefault();
  isDragOver.value = false;
  const draggedTab = readDraggedTab(e);
  if (draggedTab) {
    moveFileBetweenEditorGroups(
      draggedTab.path,
      draggedTab.sourceGroupId,
      props.groupId,
    );
    handleTabSelect(draggedTab.path);
    return;
  }
  if (!e.dataTransfer?.files?.length) return;
  const files = Array.from(e.dataTransfer.files);
  let opened = 0;
  for (const f of files) {
    // In Wails webview, dropped File objects expose the real filesystem
    // path on `path`. Fall back to `name` if unavailable (browser mode).
    const wf = f as WailsFile;
    const path = wf.path || wf.filePath || f.name;
    if (!path) continue;
    try {
      await openFileFromPath(path);
      addFileToEditorGroup(props.groupId, path, true);
      opened++;
    } catch (err) {
      notifyError(t("editor.openFailed", { path, error: err instanceof Error ? err.message : String(err) }));
    }
  }
  if (opened > 0) {
    notifySuccess(opened === 1 ? t("editor.openedOneFile") : t("editor.openedMultipleFiles", { count: opened }));
  }
}

watch(
  () => groupActiveFile.value?.language,
  (lang) => {
    if (lang) {
      appState.languageMode = lang.charAt(0).toUpperCase() + lang.slice(1);
    }
  }
);

// 文件切换时触发淡入动画（不重新挂载 Monaco）
watch(
  () => groupActiveFile.value?.path,
  () => {
    if (!groupActiveFile.value) return;
    if (switchTimer) window.clearTimeout(switchTimer);
    isFileSwitching.value = false;
    // 强制重新触发 keyframe 动画：先移除类再下一 tick 加上
    requestAnimationFrame(() => {
      isFileSwitching.value = true;
      switchTimer = window.setTimeout(() => {
        isFileSwitching.value = false;
      }, 280);
    });
  },
);

watch(
  groupActivePath,
  (path) => syncActiveGroupPath(path),
);

watch(
  () => editorState.openFiles.map((file) => file.path),
  (openPaths) => {
    ensureEditorGroup(props.groupId, openPaths, editorState.activeFilePath);
    for (const path of [...groupPaths.value]) {
      if (!openPaths.includes(path)) removeFileFromEditorGroup(props.groupId, path);
    }
    const globallyActivePath = editorState.activeFilePath;
    if (props.active && globallyActivePath && openPaths.includes(globallyActivePath)) {
      addFileToEditorGroup(props.groupId, globallyActivePath, true);
    }
  },
  { immediate: true },
);

watch(
  () => editorState.activeFilePath,
  (path) => {
    if (!props.active || !path) return;
    if (editorState.openFiles.some((file) => file.path === path)) {
      addFileToEditorGroup(props.groupId, path, true);
      syncActiveGroupPath(path);
    }
  },
);

watch(
  () => props.active,
  (active) => {
    if (!active) return;
    ensureEditorGroup(props.groupId, editorState.openFiles.map((file) => file.path), editorState.activeFilePath);
    syncActiveGroupPath();
  },
  { immediate: true },
);

onMounted(() => {
  // M-14: 存储 disposer 以便卸载时清除定时器并停止 watch。
  if (props.active) stopAutoSave = setupAutoSave(() => appState.autoSave, () => appState.autoSaveDelay);
  window.addEventListener("blur", handleBlur);
});

watch(
  () => props.active,
  (active) => {
    stopAutoSave?.();
    stopAutoSave = active
      ? setupAutoSave(() => appState.autoSave, () => appState.autoSaveDelay)
      : null;
  },
);

onBeforeUnmount(() => {
  // M-14: 调用 disposer 清除 autoSave 定时器并停止 watch。
  if (stopAutoSave) {
    stopAutoSave();
    stopAutoSave = null;
  }
  window.removeEventListener("blur", handleBlur);
  if (switchTimer) window.clearTimeout(switchTimer);
});
</script>

<template>
  <div
    class="editor-view"
    :class="{ 'editor-view--drag-over': isDragOver }"
    @keydown="handleKeydown"
    @dragover="handleDragOver"
    @dragleave="handleDragLeave"
    @drop="handleDrop"
  >
    <!-- Plan 11 Task 15 Step 4: 代码编辑器个性化背景层（位于内容之下）-->
    <div class="editor-view__bg" aria-hidden="true" />
    <TabBar
      :group-id="groupId"
      :group-active="active"
      @select="handleTabSelect"
      @close="handleTabClose"
    />
    <BreadcrumbBar v-if="active" />

    <div v-if="isMarkdown && hasOpenFiles" class="editor-view__toolbar">
      <button
        type="button"
        class="editor-view__preview-btn"
        :class="{ 'editor-view__preview-btn--active': showPreview }"
        @click="showPreview = !showPreview"
      >
        {{ showPreview ? t('editor.hidePreview') : t('editor.showPreview') }}
      </button>
    </div>

    <div class="editor-area">
      <div
        class="editor-area__editor"
        :class="{
          'editor-area__editor--split': showPreview && isMarkdown,
          'editor-area__editor--switching': isFileSwitching,
        }"
      >
        <Transition name="editor-fade" mode="out-in">
          <CodeEditor
            v-if="hasOpenFiles && groupActiveFile"
            key="editor"
            :path="groupActiveFile.path"
            :content="activeContent"
            :language="groupActiveFile.language"
            :group-id="groupId"
            @update:content="handleContentChange"
            @cursor-change="handleCursorChange"
          />

          <div v-else key="empty" class="editor-empty-state">
            <span class="empty-prompt">&gt;_</span>
            <p class="empty-hint">{{ t('editor.openFileToStart') }}</p>
            <p class="empty-sub">{{ t('editor.selectFileHint') }}</p>
          </div>
        </Transition>
      </div>

      <Transition name="preview-slide">
        <MarkdownContent
          v-if="showPreview && isMarkdown"
          class="editor-area__preview markdown-body"
          :html="previewHtml"
        />
      </Transition>
    </div>

    <div v-if="isDragOver" class="editor-view__drop-overlay">
      <div class="editor-view__drop-message">
        <el-icon :size="32"><Document /></el-icon>
        <span>{{ t('editor.dropFilesToOpen') }}</span>
      </div>
    </div>

    <ModalOverlay
      :visible="saveConflictState.visible"
      :title="t('editor.conflictTitle')"
      max-width="520px"
      @close="dismissSaveConflict"
    >
      <div class="editor-save-conflict">
        <h2>{{ t('editor.conflictTitle') }}</h2>
        <p>{{ t('editor.conflictDescription', { path: saveConflictState.path }) }}</p>
        <p class="editor-save-conflict__detail">{{ saveConflictState.message }}</p>
        <div class="editor-save-conflict__actions">
          <button
            type="button"
            class="editor-save-conflict__reload"
            :disabled="saveConflictState.resolving"
            @click="handleSaveConflict('reload')"
          >
            {{ t('editor.conflictReload') }}
          </button>
          <button
            type="button"
            class="editor-save-conflict__overwrite"
            :disabled="saveConflictState.resolving || !saveConflictState.diskHash"
            @click="handleSaveConflict('overwrite')"
          >
            {{ t('editor.conflictOverwrite') }}
          </button>
        </div>
      </div>
    </ModalOverlay>
  </div>
</template>

<style scoped>
.editor-view {
  position: relative;
  display: flex;
  flex-direction: column;
  width: 100%;
  height: 100%;
  background-color: var(--color-bg-base);
  color: var(--color-text-primary);
}

/* Plan 11 Task 15: 背景层已在 main.css 定义（z-index:0），需确保内容在之上 */
.editor-area,
.editor-view :deep(.editor-view__toolbar) {
  position: relative;
  z-index: 1;
}

.editor-area {
  flex: 1;
  display: flex;
  overflow: hidden;
  background: var(--color-bg-base);
}

.editor-empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 10px;
  text-align: center;
  padding: 24px;
  user-select: none;
  width: 100%;
}

.empty-prompt {
  font-family: var(--font-mono);
  font-size: 20px;
  color: var(--color-text-disabled);
  line-height: 1;
}

.empty-hint {
  margin: 0;
  font-size: 13px;
  color: var(--color-text-secondary);
}

.empty-sub {
  margin: 0;
  font-size: 12px;
  color: var(--color-text-tertiary);
}

.editor-view__toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 12px;
  background: var(--color-bg-surface-dim);
  border-bottom: 1px solid var(--color-border-subtle);
}

.editor-view__preview-btn {
  padding: 4px 10px;
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-secondary);
  font-size: 12px;
  cursor: pointer;
  transition: background-color var(--transition-fast),
              color var(--transition-fast);
}

.editor-view__preview-btn:hover {
  background: var(--color-bg-surface-container-low);
  color: var(--color-text-primary);
}

.editor-view__preview-btn--active {
  background: var(--color-primary);
  color: white;
  border-color: var(--color-primary);
}

.editor-area__editor {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
}

.editor-area__editor--split {
  border-right: 1px solid var(--color-border-subtle);
}

.editor-area__preview {
  flex: 1;
  overflow-y: auto;
  padding: 16px 20px;
  background: var(--color-bg-base);
  font-size: 14px;
  line-height: 1.6;
}

.editor-area__preview :deep(pre) {
  margin: 12px 0;
  padding: 12px 16px;
  background-color: var(--hljs-bg, #f6f8fa);
  border: 1px solid var(--color-border-default);
  border-radius: 8px;
  overflow-x: auto;
  font-size: 13px;
  line-height: 1.5;
}

.editor-area__preview :deep(code) {
  font-family: var(--font-mono);
  font-size: 13px;
}

.editor-area__preview :deep(code.hljs) {
  background: transparent;
  padding: 0;
  font-weight: 500;
}

/* 空状态与编辑器切换过渡：out-in 模式，旧元素先淡出再淡入新元素 */
.editor-fade-enter-active {
  transition: opacity 0.2s cubic-bezier(0.4, 0, 0.2, 1),
              transform 0.2s cubic-bezier(0.4, 0, 0.2, 1);
}

.editor-fade-leave-active {
  transition: opacity 0.14s ease-out;
}

.editor-fade-enter-from {
  opacity: 0;
  transform: translateY(6px);
}

.editor-fade-leave-to {
  opacity: 0;
}

/* Markdown 预览面板滑入/滑出 */
.preview-slide-enter-active {
  transition: opacity 0.24s cubic-bezier(0.4, 0, 0.2, 1),
              transform 0.24s cubic-bezier(0.4, 0, 0.2, 1);
}

.preview-slide-leave-active {
  transition: opacity 0.18s ease-out,
              transform 0.18s ease-out;
}

.preview-slide-enter-from {
  opacity: 0;
  transform: translateX(12px);
}

.preview-slide-leave-to {
  opacity: 0;
  transform: translateX(12px);
}

/* 文件切换时编辑器淡入+滑入动画 - 通过 keyframe 触发，不重新挂载 Monaco，仅对容器做透明度与位移过渡 */
.editor-area__editor--switching {
  animation: editorFileSwitchFade 0.3s cubic-bezier(0.25, 0.1, 0.25, 1);
}

@keyframes editorFileSwitchFade {
  0% {
    opacity: 0;
    transform: translateX(10px);
  }
  100% {
    opacity: 1;
    transform: translateX(0);
  }
}

@media (prefers-reduced-motion: reduce) {
  .editor-view__preview-btn,
  .editor-fade-enter-active,
  .editor-fade-leave-active,
  .preview-slide-enter-active,
  .preview-slide-leave-active,
  .editor-area__editor--switching {
    transition: none !important;
    animation: none !important;
  }
  .editor-fade-enter-from,
  .editor-fade-leave-to,
  .preview-slide-enter-from,
  .preview-slide-leave-to {
    opacity: 1;
    transform: none;
  }
}

.editor-view--drag-over {
  outline: 2px dashed var(--color-accent-primary, #4c9aff);
  outline-offset: -4px;
}

.editor-view__drop-overlay {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  background-color: rgba(6, 7, 15, 0.55);
  pointer-events: none;
  z-index: 10;
}

.editor-view__drop-message {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
  padding: 32px 48px;
  border: 2px dashed var(--color-accent-primary, #4c9aff);
  border-radius: var(--radius-lg);
  color: var(--color-accent-primary, #4c9aff);
  font-size: 14px;
  font-weight: 500;
}

.editor-save-conflict h2 {
  margin: 0 0 10px;
  font-size: 18px;
}

.editor-save-conflict p {
  margin: 0 0 10px;
  overflow-wrap: anywhere;
  color: var(--color-text-secondary);
}

.editor-save-conflict__detail {
  font-family: var(--font-mono);
  font-size: 12px;
}

.editor-save-conflict__actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 20px;
}

.editor-save-conflict__actions button {
  min-height: 32px;
  padding: 6px 12px;
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-sm);
  background: var(--color-bg-surface-container-low);
  color: var(--color-text-primary);
  cursor: pointer;
}

.editor-save-conflict__actions .editor-save-conflict__overwrite {
  border-color: var(--color-error, #c62828);
  color: var(--color-error, #c62828);
}

.editor-save-conflict__actions button:disabled {
  cursor: not-allowed;
  opacity: 0.55;
}
</style>
