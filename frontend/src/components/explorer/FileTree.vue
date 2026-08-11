<script setup lang="ts">
// Koyori IDE 组件 · File Tree；交互服务：文件系统（FileService）、项目（ProjectService）。
// 喵，这是 File Tree，负责 Koyori IDE 的界面呈现喵~
import {
  computed,
  nextTick,
  onBeforeUnmount,
  ref,
  shallowReactive,
  toRef,
  watch,
  type ComponentPublicInstance,
} from "vue";
import { fileService, projectService } from "@/api/services";
import { createSession } from "@/stores/terminal";
import { appState } from "@/stores/app";
import { closeFilesUnder, renameOpenFilesUnder } from "@/stores/editor";
import { fileTreeRefreshState } from "@/stores/fileTreeRefresh";
import type { DirEntry } from "@/types";
import { CaretRight, Folder, Document } from "@element-plus/icons-vue";
import { ElMessage, ElMessageBox } from "element-plus";
import { errorMessage as errorToString, isCancellationError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";

const { t } = useI18n();

/** prompt-4 D8: enable windowing only for genuinely large project branches. */
const VIRTUALIZE_THRESHOLD = 1000;
const ROW_HEIGHT = 26;
const VIEWPORT_HEIGHT = 400;
const OVERSCAN = 8;
const SEARCH_DEBOUNCE_MS = 300;
const WATCH_POLL_MS = 1000;
const WATCH_DEBOUNCE_MS = 150;

interface FileTreeNodeState {
  expanded: boolean;
  loading: boolean;
  loaded: boolean;
  errorMessage: string | null;
  children: DirEntry[];
}

type FileTreeStateCache = Map<string, FileTreeNodeState>;

const props = withDefaults(defineProps<{
  path: string;
  name: string;
  depth?: number;
  isDir?: boolean;
  searchQuery?: string;
  stateCache?: FileTreeStateCache;
}>(), {
  depth: 0,
  isDir: true,
  searchQuery: "",
});

const emit = defineEmits<{
  (e: "select", path: string): void;
  (e: "deleted", path: string): void;
  (e: "renamed", oldPath: string, newPath: string, newName: string): void;
}>();

// Virtual rows are unmounted as they leave the window. Keep recursive folder
// state in a cache owned by this root instance so expansion and loaded children
// survive windowing without leaking across separate FileTree instances.
const ownsStateCache = props.stateCache === undefined;
const stateCache = props.stateCache ?? new Map<string, FileTreeNodeState>();
let nodeState = stateCache.get(props.path);
if (!nodeState) {
  nodeState = shallowReactive<FileTreeNodeState>({
    expanded: false,
    loading: false,
    loaded: false,
    errorMessage: null,
    children: [],
  });
  stateCache.set(props.path, nodeState);
}
const expanded = toRef(nodeState, "expanded");
const loading = toRef(nodeState, "loading");
const loaded = toRef(nodeState, "loaded");
const errorMessage = toRef(nodeState, "errorMessage");
// shallowReactive avoids deeply proxying immutable 10k-entry payloads.
const children = toRef(nodeState, "children");
const scrollTop = ref(0);
const virtualViewport = ref<HTMLElement | null>(null);
const virtualViewportHeight = ref(VIEWPORT_HEIGHT);
const virtualChildHeights = new Map<string, number>();
const virtualHeightRevision = ref(0);
const virtualChildElements = new Map<string, HTMLElement>();
let virtualResizeObserver: ResizeObserver | null = null;
const searchInput = ref("");
const debouncedSearchQuery = ref("");
let searchDebounceTimer: ReturnType<typeof setTimeout> | null = null;
let watchPollTimer: ReturnType<typeof setInterval> | null = null;
let watchDebounceTimer: ReturnType<typeof setTimeout> | null = null;
let watchPollRunning = false;
let disposed = false;
const pendingWatchUpdates = new Map<string, DirEntry[]>();

watch(searchInput, (value) => {
  if (searchDebounceTimer) clearTimeout(searchDebounceTimer);
  searchDebounceTimer = setTimeout(() => {
    debouncedSearchQuery.value = value.trim();
    scrollTop.value = 0;
    if (virtualViewport.value) virtualViewport.value.scrollTop = 0;
    searchDebounceTimer = null;
  }, SEARCH_DEBOUNCE_MS);
});

const effectiveSearchQuery = computed(() =>
  (props.depth === 0 ? debouncedSearchQuery.value : props.searchQuery)
    .trim()
    .toLocaleLowerCase(),
);

const filteredChildren = computed(() => {
  const query = effectiveSearchQuery.value;
  if (!query) return children.value;
  return children.value.filter((child) =>
    child.isDir ||
    child.name.toLocaleLowerCase().includes(query) ||
    child.path.toLocaleLowerCase().includes(query),
  );
});

const useVirtual = computed(
  () => filteredChildren.value.length > VIRTUALIZE_THRESHOLD,
);

const virtualLayout = computed(() => {
  // Track one coarse revision instead of 10k individual reactive Map keys.
  // This keeps initial layout construction linear with low dependency overhead.
  void virtualHeightRevision.value;
  const entries = filteredChildren.value;
  const offsets = new Float64Array(entries.length + 1);
  for (let index = 0; index < entries.length; index += 1) {
    const child = entries[index];
    offsets[index + 1] = offsets[index] +
      (virtualChildHeights.get(child.path) ?? ROW_HEIGHT);
  }
  return {
    offsets,
    totalH: offsets[entries.length] ?? 0,
  };
});

function virtualIndexAtOffset(offset: number): number {
  const total = filteredChildren.value.length;
  if (total === 0) return 0;
  const { offsets } = virtualLayout.value;
  let low = 0;
  let high = total;
  while (low < high) {
    const middle = Math.floor((low + high) / 2);
    if (offsets[middle + 1] <= offset) low = middle + 1;
    else high = middle;
  }
  return Math.min(low, total - 1);
}

const virtualWindow = computed(() => {
  if (!useVirtual.value) {
    return {
      start: 0,
      end: filteredChildren.value.length,
      totalH: virtualLayout.value.totalH,
    };
  }
  const total = filteredChildren.value.length;
  const firstVisible = virtualIndexAtOffset(scrollTop.value);
  const lastVisible = virtualIndexAtOffset(
    scrollTop.value + virtualViewportHeight.value,
  );
  const start = Math.max(0, firstVisible - OVERSCAN);
  const end = Math.min(total, lastVisible + OVERSCAN + 1);
  return {
    start,
    end,
    totalH: virtualLayout.value.totalH,
  };
});

const visibleChildren = computed(() => {
  const { start, end } = virtualWindow.value;
  return filteredChildren.value.slice(start, end).map((child, offset) => {
    const actualIndex = start + offset;
    return {
      child,
      top: virtualLayout.value.offsets[actualIndex],
    };
  });
});

function onChildrenScroll(e: Event): void {
  const el = e.target as HTMLElement;
  scrollTop.value = el.scrollTop;
  if (el.clientHeight > 0) virtualViewportHeight.value = el.clientHeight;
}

function toHTMLElement(
  value: Element | ComponentPublicInstance | null,
): HTMLElement | null {
  if (value instanceof HTMLElement) return value;
  if (!value || value instanceof Element) return null;
  const componentElement = value.$el;
  return componentElement instanceof HTMLElement ? componentElement : null;
}

function recordVirtualChildHeight(path: string, rawHeight: number): void {
  if (!Number.isFinite(rawHeight) || rawHeight <= 0) return;
  const height = Math.max(ROW_HEIGHT, Math.ceil(rawHeight));
  const previous = virtualChildHeights.get(path) ?? ROW_HEIGHT;
  if (Math.abs(previous - height) < 1) return;

  const childIndex = filteredChildren.value.findIndex((child) => child.path === path);
  const viewportAnchorIndex = virtualIndexAtOffset(scrollTop.value);
  virtualChildHeights.set(path, height);
  virtualHeightRevision.value += 1;

  if (childIndex >= 0 && childIndex < viewportAnchorIndex && virtualViewport.value) {
    virtualViewport.value.scrollTop += height - previous;
    scrollTop.value = virtualViewport.value.scrollTop;
  }
}

function onVirtualChildResize(entries: ResizeObserverEntry[]): void {
  for (const entry of entries) {
    const element = entry.target as HTMLElement;
    const path = element.dataset.virtualPath;
    if (path) recordVirtualChildHeight(path, entry.contentRect.height);
  }
}

function ensureVirtualResizeObserver(): ResizeObserver | null {
  if (virtualResizeObserver || typeof ResizeObserver === "undefined") {
    return virtualResizeObserver;
  }
  virtualResizeObserver = new ResizeObserver(onVirtualChildResize);
  return virtualResizeObserver;
}

function setVirtualChildElement(
  value: Element | ComponentPublicInstance | null,
  path: string,
): void {
  const previous = virtualChildElements.get(path);
  const element = toHTMLElement(value);
  if (previous && previous !== element) virtualResizeObserver?.unobserve(previous);
  if (!element) {
    virtualChildElements.delete(path);
    return;
  }

  virtualChildElements.set(path, element);
  const observer = ensureVirtualResizeObserver();
  if (observer) {
    observer.observe(element);
    return;
  }
  void nextTick(() => {
    const height = element.getBoundingClientRect().height;
    if (height > 0) recordVirtualChildHeight(path, height);
  });
}

const contextMenuVisible = ref(false);
const contextMenuX = ref(0);
const contextMenuY = ref(0);
const contextMenuEl = ref<HTMLElement | null>(null);
let contextListenersRegistered = false;

const isFolder = computed(() => props.depth === 0 || props.isDir);
const isWorkspaceRoot = computed(() => props.depth === 0);

function withoutSelfReference(entries: DirEntry[]): DirEntry[] {
  const selfIndex = entries.findIndex((entry) => entry.path === props.path);
  if (selfIndex < 0) return entries;
  return entries.filter((entry) => entry.path !== props.path);
}

function purgeStateCacheUnder(path: string): void {
  const prefix = path.endsWith("/") || path.endsWith("\\") ? path : `${path}/`;
  const altPrefix = path.endsWith("/") || path.endsWith("\\") ? path : `${path}\\`;
  for (const key of [...stateCache.keys()]) {
    if (key === path || key.startsWith(prefix) || key.startsWith(altPrefix)) {
      stateCache.delete(key);
    }
  }
  for (const key of [...virtualChildHeights.keys()]) {
    if (key === path || key.startsWith(prefix) || key.startsWith(altPrefix)) {
      virtualChildHeights.delete(key);
    }
  }
  virtualHeightRevision.value += 1;
}

function removeChildEntry(path: string): void {
  children.value = children.value.filter((entry) => entry.path !== path);
  purgeStateCacheUnder(path);
}

function renameChildEntry(oldPath: string, newPath: string, newName: string): void {
  children.value = children.value.map((entry) =>
    entry.path === oldPath ? { ...entry, path: newPath, name: newName } : entry,
  );
  purgeStateCacheUnder(oldPath);
  const height = virtualChildHeights.get(oldPath);
  if (height !== undefined) {
    virtualChildHeights.delete(oldPath);
    virtualChildHeights.set(newPath, height);
  }
  virtualHeightRevision.value += 1;
}

function entriesMatch(left: DirEntry[], right: DirEntry[]): boolean {
  return left.length === right.length && left.every((entry, index) => {
    const other = right[index];
    return other && entry.path === other.path && entry.isDir === other.isDir &&
      entry.size === other.size && entry.modified === other.modified;
  });
}

function applyPendingWatchUpdates(): void {
  for (const [parentPath, entries] of pendingWatchUpdates) {
    const state = stateCache.get(parentPath);
    if (!state?.loaded) continue;
    const nextPaths = new Set(entries.map((entry) => entry.path));
    for (const previous of state.children) {
      if (!nextPaths.has(previous.path)) {
        closeFilesUnder(previous.path);
        purgeStateCacheUnder(previous.path);
      }
    }
    state.children = entries;
  }
  pendingWatchUpdates.clear();
  watchDebounceTimer = null;
}

async function pollLoadedDirectories(): Promise<void> {
  if (watchPollRunning || disposed) return;
  watchPollRunning = true;
  try {
    const loadedDirectories = [...stateCache.entries()]
      .filter(([, state]) => state.loaded);
    for (const [path, state] of loadedDirectories) {
      try {
        const entries = withoutSelfReference(await fileService.listDirectory(path));
        if (!entriesMatch(state.children, entries)) pendingWatchUpdates.set(path, entries);
      } catch (err) {
        console.warn("File watcher refresh failed:", err);
      }
    }
    if (!disposed && pendingWatchUpdates.size > 0 && !watchDebounceTimer) {
      watchDebounceTimer = setTimeout(applyPendingWatchUpdates, WATCH_DEBOUNCE_MS);
    }
  } finally {
    watchPollRunning = false;
  }
}

function ensureWorkspaceWatcher(): void {
  if (!ownsStateCache || watchPollTimer) return;
  watchPollTimer = setInterval(() => void pollLoadedDirectories(), WATCH_POLL_MS);
}

watch(
  () => fileTreeRefreshState.revision,
  () => {
    if (ownsStateCache && !disposed) void pollLoadedDirectories();
  },
);

function onWindowPointerDown(event: PointerEvent): void {
  if (!contextMenuVisible.value) return;
  const target = event.target;
  if (target instanceof Node && contextMenuEl.value?.contains(target)) return;
  closeContextMenu();
}

function onWindowKeydown(event: KeyboardEvent): void {
  if (event.key === "Escape" && contextMenuVisible.value) {
    event.preventDefault();
    closeContextMenu();
  }
}

function ensureContextListeners(): void {
  if (contextListenersRegistered) return;
  window.addEventListener("pointerdown", onWindowPointerDown, true);
  window.addEventListener("keydown", onWindowKeydown, true);
  contextListenersRegistered = true;
}

function teardownContextListeners(): void {
  if (!contextListenersRegistered) return;
  window.removeEventListener("pointerdown", onWindowPointerDown, true);
  window.removeEventListener("keydown", onWindowKeydown, true);
  contextListenersRegistered = false;
}

async function toggle() {
  if (expanded.value) {
    expanded.value = false;
    return;
  }
  expanded.value = true;
  if (loaded.value || loading.value) {
    return;
  }
  loading.value = true;
  errorMessage.value = null;
  try {
    children.value = withoutSelfReference(await fileService.listDirectory(props.path));
    loaded.value = true;
    ensureWorkspaceWatcher();
  } catch (err) {
    errorMessage.value = err instanceof Error ? err.message : String(err);
    console.error("Failed to list directory:", err);
  } finally {
    loading.value = false;
  }
}

function onRowClick() {
  if (isFolder.value) {
    toggle();
  } else {
    emit("select", props.path);
  }
}

function onContextMenu(e: MouseEvent) {
  e.preventDefault();
  e.stopPropagation();
  contextMenuX.value = e.clientX;
  contextMenuY.value = e.clientY;
  contextMenuVisible.value = true;
  ensureContextListeners();
}

function closeContextMenu() {
  contextMenuVisible.value = false;
  teardownContextListeners();
}

async function handleNewFile() {
  closeContextMenu();
  if (!isFolder.value) return;
  try {
    const { value } = await ElMessageBox.prompt(t("fileTree.fileNamePrompt"), t("fileTree.newFile"), {
      confirmButtonText: t("fileTree.create"),
      cancelButtonText: t("common.cancel"),
    });
    if (!value) return;
    const newPath = props.path + "/" + value;
    await fileService.createFile(newPath);
    if (!expanded.value) expanded.value = true;
    await reloadChildren();
    emit("select", newPath);
  } catch (e: unknown) {
    if (isCancellationError(e)) return;
    ElMessage.error(t("fileTree.failedAction", { error: errorToString(e) }));
  }
}

async function handleNewFolder() {
  closeContextMenu();
  if (!isFolder.value) return;
  try {
    const { value } = await ElMessageBox.prompt(t("fileTree.folderNamePrompt"), t("fileTree.newFolder"), {
      confirmButtonText: t("fileTree.create"),
      cancelButtonText: t("common.cancel"),
    });
    if (!value) return;
    const newPath = props.path + "/" + value;
    await fileService.createDirectory(newPath);
    if (!expanded.value) expanded.value = true;
    await reloadChildren();
  } catch (e: unknown) {
    if (isCancellationError(e)) return;
    ElMessage.error(t("fileTree.failedAction", { error: errorToString(e) }));
  }
}

async function handleQuickNewFile(extension: string): Promise<void> {
  closeContextMenu();
  if (!isFolder.value) return;
  try {
    const { value } = await ElMessageBox.prompt(
      t("fileTree.fileNamePrompt"),
      `${t("fileTree.newFile")} ${extension}`,
      {
        confirmButtonText: t("fileTree.create"),
        cancelButtonText: t("common.cancel"),
        inputValue: `untitled${extension}`,
      },
    );
    if (!value) return;
    const newPath = `${props.path}/${value}`;
    await fileService.createFile(newPath);
    if (!expanded.value) expanded.value = true;
    await reloadChildren();
    emit("select", newPath);
  } catch (e: unknown) {
    if (isCancellationError(e)) return;
    ElMessage.error(t("fileTree.failedAction", { error: errorToString(e) }));
  }
}

async function handleNewProject(templateId: string): Promise<void> {
  closeContextMenu();
  if (!isFolder.value) return;
  try {
    const { value } = await ElMessageBox.prompt(
      t("newProject.projectName"),
      `${t("newProject.title")} (${templateId})`,
      {
        confirmButtonText: t("fileTree.create"),
        cancelButtonText: t("common.cancel"),
        inputValue: templateId === "uniapp" ? "uni-app-project" : `${templateId}-project`,
      },
    );
    if (!value) return;
    await projectService.createProject({
      templateId,
      projectName: value.trim(),
      targetDir: props.path,
      moduleName: "",
    });
    await reloadChildren();
    ElMessage.success(t("newProject.created"));
  } catch (e: unknown) {
    if (isCancellationError(e)) return;
    ElMessage.error(t("fileTree.failedAction", { error: errorToString(e) }));
  }
}

async function handleRename() {
  closeContextMenu();
  if (isWorkspaceRoot.value) return;
  try {
    const { value } = await ElMessageBox.prompt(t("fileTree.newNamePrompt"), t("fileTree.renameTitle"), {
      confirmButtonText: t("fileTree.rename"),
      cancelButtonText: t("common.cancel"),
      inputValue: props.name,
    });
    if (!value || value === props.name) return;
    const slash = Math.max(props.path.lastIndexOf("/"), props.path.lastIndexOf("\\"));
    const parentPath = slash >= 0 ? props.path.substring(0, slash) : "";
    const sep = props.path.includes("\\") && !props.path.includes("/") ? "\\" : "/";
    const newPath = parentPath ? `${parentPath}${sep}${value}` : value;
    await fileService.renamePath(props.path, newPath);
    renameOpenFilesUnder(props.path, newPath);
    emit("renamed", props.path, newPath, value);
    emit("select", newPath);
  } catch (e: unknown) {
    if (isCancellationError(e)) return;
    ElMessage.error(t("fileTree.failedAction", { error: errorToString(e) }));
  }
}

async function handleDelete() {
  closeContextMenu();
  if (isWorkspaceRoot.value) return;
  try {
    await ElMessageBox.confirm(
      t("fileTree.deleteConfirm", { name: props.name }),
      t("fileTree.confirmDeleteTitle"),
      { confirmButtonText: t("fileTree.delete"), cancelButtonText: t("common.cancel"), type: "warning" }
    );
    const deletedPath = props.path;
    await fileService.deletePath(deletedPath);
    closeFilesUnder(deletedPath);
    emit("deleted", deletedPath);
  } catch (e: unknown) {
    if (isCancellationError(e)) return;
    ElMessage.error(t("fileTree.failedAction", { error: errorToString(e) }));
  }
}

function onChildDeleted(path: string): void {
  removeChildEntry(path);
  emit("deleted", path);
}

function onChildRenamed(oldPath: string, newPath: string, newName: string): void {
  renameChildEntry(oldPath, newPath, newName);
  emit("renamed", oldPath, newPath, newName);
}

async function handleCopyPath() {
  closeContextMenu();
  try {
    await navigator.clipboard.writeText(props.path);
    ElMessage.success(t("fileTree.pathCopied"));
  } catch {
    ElMessage.error(t("fileTree.failedCopyPath"));
  }
}

async function handleOpenInTerminal() {
  closeContextMenu();
  // For a folder, use its own path; for a file, use the parent directory.
  const targetDir = isFolder.value
    ? props.path
    : props.path.substring(0, props.path.lastIndexOf("/"));
  if (!targetDir) {
    ElMessage.error(t("fileTree.cannotResolveDir"));
    return;
  }
  // Reveal the bottom panel so the user sees the new terminal.
  appState.terminalVisible = true;
  try {
    const id = await createSession(targetDir);
    if (!id) {
      ElMessage.error(t("fileTree.failedOpenTerminal"));
    }
  } catch (e: unknown) {
    ElMessage.error(t("fileTree.failedOpenTerminalError", { error: errorToString(e) }));
  }
}

async function handleRevealInOS() {
  closeContextMenu();
  try {
    await fileService.revealInOS(props.path);
  } catch (e: unknown) {
    ElMessage.error(t("fileTree.failedReveal", { error: errorToString(e) }));
  }
}

async function handleRefresh() {
  closeContextMenu();
  if (!isFolder.value) return;
  if (!expanded.value) expanded.value = true;
  await reloadChildren();
}

async function reloadChildren() {
  loaded.value = false;
  loading.value = true;
  try {
    children.value = withoutSelfReference(await fileService.listDirectory(props.path));
    loaded.value = true;
  } catch (err) {
    errorMessage.value = err instanceof Error ? err.message : String(err);
  } finally {
    loading.value = false;
  }
}

const indent = { paddingLeft: `${props.depth * 12 + 8}px` };

onBeforeUnmount(() => {
  disposed = true;
  teardownContextListeners();
  if (searchDebounceTimer) {
    clearTimeout(searchDebounceTimer);
    searchDebounceTimer = null;
  }
  if (watchPollTimer) {
    clearInterval(watchPollTimer);
    watchPollTimer = null;
  }
  if (watchDebounceTimer) {
    clearTimeout(watchDebounceTimer);
    watchDebounceTimer = null;
  }
  pendingWatchUpdates.clear();
  virtualResizeObserver?.disconnect();
  virtualResizeObserver = null;
  virtualChildElements.clear();
  virtualChildHeights.clear();
  if (ownsStateCache) stateCache.clear();
});
</script>

<template>
  <div class="file-tree">
    <div v-if="depth === 0" class="file-tree__search">
      <input
        v-model="searchInput"
        type="search"
        class="file-tree__search-input"
        :placeholder="t('search.placeholder')"
        :aria-label="t('search.queryAria')"
      />
    </div>
    <button
      type="button"
      class="file-tree__row"
      :style="indent"
      :aria-expanded="isFolder ? expanded : undefined"
      @click="onRowClick"
      @keydown.enter.prevent="onRowClick"
      @keydown.space.prevent="onRowClick"
      @contextmenu="onContextMenu"
    >
      <span
        v-if="isFolder && depth > 0"
        class="file-tree__chevron"
        :class="{ 'file-tree__chevron--expanded': expanded }"
        aria-hidden="true"
      >
        <el-icon :size="12"><CaretRight /></el-icon>
      </span>
      <span v-else class="file-tree__chevron-placeholder" />

      <el-icon :size="14" class="file-tree__icon">
        <Folder v-if="isFolder" />
        <Document v-else />
      </el-icon>

      <span class="file-tree__name">{{ name }}</span>
    </button>

    <div v-if="expanded && loading" class="file-tree__loading">
      {{ t("fileTree.loading") }}
    </div>

    <div v-if="expanded && errorMessage" class="file-tree__error">
      {{ errorMessage }}
    </div>

    <!-- prompt-5 Task J: virtualize large directories to keep DOM bounded -->
    <div
      v-if="expanded && !loading && !errorMessage && useVirtual"
      ref="virtualViewport"
      class="file-tree__children file-tree__children--virtual"
      :style="{ height: VIEWPORT_HEIGHT + 'px' }"
      @scroll.passive="onChildrenScroll"
    >
      <div class="file-tree__virt-spacer" :style="{ height: virtualWindow.totalH + 'px' }">
        <div
          v-for="{ child, top } in visibleChildren"
          :key="child.path"
          v-memo="[child.path, child.isDir, top]"
          :ref="(element) => setVirtualChildElement(element, child.path)"
          class="file-tree__virtual-item"
          :data-virtual-path="child.path"
          :style="{ transform: `translateY(${top}px)` }"
        >
          <FileTree
            :path="child.path"
            :name="child.name"
            :is-dir="child.isDir"
            :depth="depth + 1"
            :search-query="effectiveSearchQuery"
            :state-cache="stateCache"
            @select="emit('select', $event)"
            @deleted="onChildDeleted"
            @renamed="onChildRenamed"
          />
        </div>
      </div>
    </div>
    <div v-else-if="expanded && !loading && !errorMessage" class="file-tree__children">
      <FileTree
        v-for="child in filteredChildren"
        :key="child.path"
        :path="child.path"
        :name="child.name"
        :is-dir="child.isDir"
        :depth="depth + 1"
        :search-query="effectiveSearchQuery"
        :state-cache="stateCache"
        @select="emit('select', $event)"
        @deleted="onChildDeleted"
        @renamed="onChildRenamed"
      />
    </div>

    <Teleport to="body">
      <div
        v-if="contextMenuVisible"
        ref="contextMenuEl"
        class="file-tree__context-menu"
        role="menu"
        :style="{ left: contextMenuX + 'px', top: contextMenuY + 'px' }"
        @contextmenu.prevent="closeContextMenu"
        @pointerdown.stop
      >
        <button type="button" v-if="isFolder" class="ctx-item" @click="handleNewFile">{{ t("fileTree.newFile") }}</button>
        <button type="button" v-if="isFolder" class="ctx-item" @click="handleNewFolder">{{ t("fileTree.newFolder") }}</button>
        <button type="button" v-if="isFolder" class="ctx-item" @click="handleRefresh">{{ t("fileTree.refresh") }}</button>
        <div v-if="isFolder" class="ctx-section-label">Quick create</div>
        <button v-if="isFolder" type="button" class="ctx-item" @click="handleQuickNewFile('.cpp')">{{ t("fileTree.quickCpp") }}</button>
        <button v-if="isFolder" type="button" class="ctx-item" @click="handleQuickNewFile('.h')">{{ t("fileTree.quickHeader") }}</button>
        <button v-if="isFolder" type="button" class="ctx-item" @click="handleQuickNewFile('.py')">{{ t("fileTree.quickPython") }}</button>
        <button v-if="isFolder" type="button" class="ctx-item" @click="handleQuickNewFile('.ts')">{{ t("fileTree.quickTypeScript") }}</button>
        <button v-if="isFolder" type="button" class="ctx-item" @click="handleQuickNewFile('.tsx')">{{ t("fileTree.quickReact") }}</button>
        <button v-if="isFolder" type="button" class="ctx-item" @click="handleQuickNewFile('.vue')">{{ t("fileTree.quickVue") }}</button>
        <button v-if="isFolder" type="button" class="ctx-item" @click="handleQuickNewFile('.html')">{{ t("fileTree.quickHtml") }}</button>
        <button v-if="isFolder" type="button" class="ctx-item" @click="handleQuickNewFile('.css')">{{ t("fileTree.quickCss") }}</button>
        <button v-if="isFolder" type="button" class="ctx-item" @click="handleQuickNewFile('.json')">{{ t("fileTree.quickJson") }}</button>
        <div v-if="isFolder" class="ctx-section-label">{{ t("fileTree.projectTemplates") }}</div>
        <button v-if="isFolder" type="button" class="ctx-item" @click="handleNewProject('html')">{{ t("fileTree.templateHtml") }}</button>
        <button v-if="isFolder" type="button" class="ctx-item" @click="handleNewProject('vue')">{{ t("fileTree.templateVue") }}</button>
        <button v-if="isFolder" type="button" class="ctx-item" @click="handleNewProject('uniapp')">{{ t("fileTree.templateUniapp") }}</button>
        <button v-if="!isWorkspaceRoot" type="button" class="ctx-item" @click="handleRename">{{ t("fileTree.rename") }}</button>
        <button v-if="!isWorkspaceRoot" type="button" class="ctx-item ctx-item--danger" @click="handleDelete">{{ t("fileTree.delete") }}</button>
        <button type="button" class="ctx-item" @click="handleCopyPath">{{ t("fileTree.copyPath") }}</button>
        <button type="button" class="ctx-item" @click="handleOpenInTerminal">{{ t("fileTree.openInTerminal") }}</button>
        <button type="button" class="ctx-item" @click="handleRevealInOS">{{ t("fileTree.revealInExplorer") }}</button>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.file-tree__search {
  padding: 4px 8px 6px;
}

.file-tree__search-input {
  width: 100%;
  height: 26px;
  padding: 0 8px;
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-xs, 4px);
  outline: none;
  color: var(--color-text-primary);
  background: var(--color-bg-surface-container-low);
  font: 12px var(--font-sans);
}

.file-tree__search-input:focus-visible {
  border-color: var(--color-accent);
  outline: 2px solid var(--color-accent);
  outline-offset: -1px;
}

.file-tree__row {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  height: 26px;
  padding-top: 0;
  padding-right: 8px;
  padding-bottom: 0;
  border: 0;
  cursor: pointer;
  user-select: none;
  color: inherit;
  background: transparent;
  font: inherit;
  text-align: left;
  border-radius: var(--radius-sm);
  transition: background-color var(--transition-fast, 150ms var(--ease-standard));
}

.file-tree__row:hover {
  background-color: var(--color-bg-surface-container-low);
}

.file-tree__row:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: -2px;
}

.file-tree__chevron {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  color: var(--color-text-tertiary);
  transition: transform var(--transition-fast, 150ms var(--ease-standard));
}

.file-tree__chevron--expanded {
  transform: rotate(90deg);
}

.file-tree__chevron-placeholder {
  width: 16px;
  flex-shrink: 0;
}

.file-tree__icon {
  color: var(--color-text-tertiary);
  flex-shrink: 0;
}

.file-tree__children--virtual {
  overflow-y: auto;
  overflow-x: hidden;
}

.file-tree__virt-spacer {
  position: relative;
  width: 100%;
}

.file-tree__virtual-item {
  position: absolute;
  top: 0;
  left: 0;
  width: 100%;
  will-change: transform;
}

.file-tree__name {
  font-size: 12px;
  color: var(--color-text-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.file-tree__loading {
  padding: 4px 12px;
  font-size: 11px;
  color: var(--color-text-tertiary);
}

.file-tree__error {
  padding: 4px 12px;
  font-size: 11px;
  color: var(--color-error, var(--color-text-tertiary));
}

.file-tree__children {
  /* children render with their own indentation */
}

.file-tree__context-menu {
  position: fixed;
  z-index: 9999;
  min-width: 180px;
  max-height: min(520px, calc(100vh - 16px));
  overflow-y: auto;
  padding: 4px;
  background: var(--color-bg-elevated);
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-sm);
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.4);
}

.ctx-section-label {
  padding: 8px 10px 3px;
  border-top: 1px solid var(--color-border-subtle);
  color: var(--color-text-tertiary);
  font-size: 10px;
  font-weight: 600;
  text-transform: uppercase;
}

.ctx-item {
  display: block;
  width: 100%;
  padding: 6px 10px;
  font-size: 12px;
  font-family: var(--font-sans);
  color: var(--color-text-secondary);
  background: transparent;
  border: none;
  border-radius: var(--radius-xs);
  text-align: left;
  cursor: pointer;
}

.ctx-item:hover {
  background: var(--color-bg-surface-container-low);
  color: var(--color-text-primary);
}

.ctx-item--danger:hover {
  color: var(--color-error, #f87171);
}
</style>
