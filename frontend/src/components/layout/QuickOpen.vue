<script setup lang="ts">
// Koyori IDE 组件 · Quick Open；交互服务：文件系统（FileService）。
// 喵，这是 Quick Open，负责 Koyori IDE 的界面呈现喵~
// Plan 55: Quick Open (Ctrl+P) fuzzy file finder.
//
// Mirrors the CommandPalette overlay pattern but sources its list from
// fileService.listAllFiles and uses the fuzzy matcher in lib/fuzzy.ts.
// On open, it fetches the file list for the current project (cached for
// the session). Selecting a file reads its content and calls openFile.

import { ref, computed, watch, nextTick, onBeforeUnmount } from "vue";
import { appState } from "@/stores/app";
import { openFile } from "@/stores/editor";
import { fileService } from "@/api/services";
import { errorMessage } from "@/lib/errors";
import { notifyError } from "@/lib/notifications";
import { fuzzyFilter, basename, dirname } from "@/lib/fuzzy";
import { useI18n } from "@/lib/i18n";

const props = defineProps<{
  visible: boolean;
}>();

const emit = defineEmits<{
  (e: "close"): void;
}>();

const { t } = useI18n();

const query = ref("");
const debouncedQuery = ref("");
const selectedIndex = ref(0);
const inputRef = ref<HTMLInputElement | null>(null);
const dialogRef = ref<HTMLElement | null>(null);
const loading = ref(false);
const allFiles = ref<string[]>([]);
// N-126: remember the element that had focus before opening so we can
// restore it when the dialog closes.
let previouslyFocused: HTMLElement | null = null;
// Cache the file list per project root so re-opening Quick Open is instant
// within the same project. The cache is invalidated when the project changes.
const cachedProjectRoot = ref<string>("");
let searchTimer: ReturnType<typeof setTimeout> | null = null;

const filtered = computed(() =>
  fuzzyFilter(allFiles.value, debouncedQuery.value.trim(), 200),
);

async function ensureFilesLoaded() {
  const root = appState.currentProject;
  if (!root) return;
  if (cachedProjectRoot.value === root && allFiles.value.length > 0) return;
  loading.value = true;
  try {
    const files = await fileService.listAllFiles(root);
    allFiles.value = files;
    cachedProjectRoot.value = root;
  } catch (e) {
    notifyError(t("quickOpen.errorLoad"), errorMessage(e));
    allFiles.value = [];
  } finally {
    loading.value = false;
  }
}

watch(
  () => props.visible,
  (v) => {
    if (v) {
      // N-126: capture the trigger element so we can restore focus on close.
      previouslyFocused = document.activeElement as HTMLElement | null;
      query.value = "";
      debouncedQuery.value = "";
      selectedIndex.value = 0;
      nextTick(() => inputRef.value?.focus());
      ensureFilesLoaded();
    } else {
      // N-126: restore focus to the trigger element on close.
      previouslyFocused?.focus?.();
      previouslyFocused = null;
    }
  },
  { immediate: true },
);

watch(query, (value) => {
  if (searchTimer) {
    clearTimeout(searchTimer);
    searchTimer = null;
  }
  if (!value.trim()) {
    debouncedQuery.value = "";
    return;
  }
  searchTimer = setTimeout(() => {
    searchTimer = null;
    debouncedQuery.value = value;
  }, 300);
});

watch(filtered, () => {
  selectedIndex.value = 0;
});

async function openSelected(path: string) {
  const root = appState.currentProject;
  if (!root) return;
  try {
    // Construct the full path using the OS-aware separator. The backend
    // returns relative paths with forward slashes; the editor store's
    // openFile expects an absolute path.
    const sep = root.includes("\\") ? "\\" : "/";
    const fullPath = root + sep + path.replace(/\//g, sep);
    const content = await fileService.readFile(fullPath);
    openFile(fullPath, content);
  } catch (e) {
    notifyError(t("quickOpen.errorOpen"), errorMessage(e));
  } finally {
    emit("close");
  }
}

function handleKeydown(e: KeyboardEvent) {
  if (e.key === "ArrowDown") {
    e.preventDefault();
    if (filtered.value.length === 0) return;
    selectedIndex.value = Math.min(
      selectedIndex.value + 1,
      filtered.value.length - 1,
    );
  } else if (e.key === "ArrowUp") {
    e.preventDefault();
    if (filtered.value.length === 0) return;
    selectedIndex.value = Math.max(selectedIndex.value - 1, 0);
  } else if (e.key === "Enter") {
    e.preventDefault();
    if (searchTimer) {
      clearTimeout(searchTimer);
      searchTimer = null;
      debouncedQuery.value = query.value;
    }
    const match = filtered.value[selectedIndex.value];
    if (match) openSelected(match.path);
  } else if (e.key === "Escape") {
    emit("close");
  }
}

// N-126: focus trap —when Tab/Shift+Tab is pressed inside the dialog,
// cycle focus among the dialog's focusable elements instead of letting
// it escape to the underlying UI.
function handleTab(e: KeyboardEvent) {
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
  if (e.shiftKey) {
    if (document.activeElement === first || document.activeElement === root) {
      e.preventDefault();
      last.focus();
    }
  } else {
    if (document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  }
}

onBeforeUnmount(() => {
  if (searchTimer) clearTimeout(searchTimer);
});
</script>

<template>
  <transition name="qo-fade">
    <div
      v-if="visible"
      class="quick-open-overlay"
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
        class="quick-open"
        role="dialog"
        aria-modal="true"
        :aria-label="t('quickOpen.title')"
        tabindex="-1"
        @keydown.tab="handleTab"
      >
        <input
          ref="inputRef"
          v-model="query"
          class="quick-open__input"
          :placeholder="t('quickOpen.placeholder')"
          :aria-label="t('quickOpen.inputAria')"
          role="combobox"
          aria-expanded="true"
          :aria-activedescendant="selectedIndex >= 0 && filtered[selectedIndex] ? `qo-item-${selectedIndex}` : undefined"
          @keydown="handleKeydown"
        />
        <div
          class="quick-open__list"
          role="listbox"
          :aria-label="t('quickOpen.title')"
          aria-live="polite"
        >
          <div v-if="loading" class="quick-open__empty">
            {{ t("quickOpen.loading") }}
          </div>
          <div v-else-if="filtered.length === 0" class="quick-open__empty">
            {{ t("quickOpen.noFiles") }}
          </div>
          <button
            v-for="(m, i) in filtered"
            :id="`qo-item-${i}`"
            :key="m.path"
            type="button"
            class="quick-open__item"
            :class="{ 'quick-open__item--active': i === selectedIndex }"
            role="option"
            tabindex="-1"
            :aria-selected="i === selectedIndex"
            @click="openSelected(m.path)"
            @mouseenter="selectedIndex = i"
          >
            <span class="quick-open__name">{{ basename(m.path) }}</span>
            <span class="quick-open__dir">{{ dirname(m.path) }}</span>
          </button>
        </div>
      </div>
    </div>
  </transition>
</template>

<style scoped>
.quick-open-overlay {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background-color: rgba(0, 0, 0, 0.4);
  z-index: 1000;
  display: flex;
  justify-content: center;
  align-items: flex-start;
  padding-top: 80px;
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

.quick-open {
  position: relative;
  z-index: 1;
  width: 560px;
  max-width: 90vw;
  background-color: var(--color-bg-surface);
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-md);
  overflow: hidden;
  box-shadow: 0 8px 32px rgba(0, 0, 0, 0.3);
}

.quick-open__input {
  width: 100%;
  padding: 12px 16px;
  font-size: 14px;
  font-family: var(--font-sans);
  color: var(--color-text-primary);
  background-color: transparent;
  border: none;
  border-bottom: 1px solid var(--color-border-subtle);
  outline: none;
}

.quick-open__input::placeholder {
  color: var(--color-text-tertiary);
}

.quick-open__list {
  max-height: 360px;
  overflow-y: auto;
  padding: 4px;
}

.quick-open__empty {
  padding: 16px;
  font-size: 12px;
  color: var(--color-text-tertiary);
  text-align: center;
}

.quick-open__item {
  display: flex;
  flex-direction: column;
  width: 100%;
  padding: 8px 12px;
  background: transparent;
  border: none;
  border-radius: var(--radius-sm);
  cursor: pointer;
  text-align: left;
  color: var(--color-text-primary);
  transition: background-color 80ms ease;
}

.quick-open__item--active {
  background-color: color-mix(in srgb, var(--color-primary) 12%, transparent);
}

.quick-open__name {
  font-size: 13px;
  font-weight: 500;
}

.quick-open__dir {
  font-size: 11px;
  color: var(--color-text-tertiary);
  font-family: var(--font-mono);
  margin-top: 2px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.qo-fade-enter-active,
.qo-fade-leave-active {
  transition: opacity 120ms ease;
}

.qo-fade-enter-from,
.qo-fade-leave-to {
  opacity: 0;
}
</style>
