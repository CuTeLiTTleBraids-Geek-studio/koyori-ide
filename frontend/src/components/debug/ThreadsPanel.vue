<script setup lang="ts">
// Koyori IDE 组件 · Threads Panel。
// 喵，这是 Threads Panel，负责 Koyori IDE 的界面呈现喵~
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import {
  BottomRight,
  CaretRight,
  Loading,
  Refresh,
  RefreshRight,
  TopRight,
  VideoPause,
  VideoPlay,
} from "@element-plus/icons-vue";
import { useI18n } from "@/lib/i18n";
import {
  acquireDebugThreadsActivation,
  continueAllDebugThreads,
  continueDebugThread,
  debugThreadsState,
  listDebugThreads,
  pauseAllDebugThreads,
  selectDebugThread,
  stepDebugThread,
  toggleDebugThreadExpanded,
  type DebugStackFrame,
  type DebugThreadInfo,
  type DebugThreadState,
} from "@/lib/debugThreads";

const props = withDefaults(defineProps<{
  sessionId: string;
  autoActivate?: boolean;
}>(), {
  autoActivate: true,
});

const emit = defineEmits<{
  selectFrame: [frame: DebugStackFrame, thread: DebugThreadInfo];
  selectThread: [thread: DebugThreadInfo];
  "goto-frame": [frame: DebugStackFrame, thread: DebugThreadInfo];
}>();

const { t } = useI18n();
const panel = ref<HTMLElement | null>(null);
const focusedThreadId = ref<number | null>(null);
let releaseActivation: (() => void) | null = null;

const threads = computed(() => {
  if (!props.sessionId) return [];
  if (debugThreadsState.sessionId === props.sessionId) {
    return debugThreadsState.threads;
  }
  return [];
});
const panelLoading = computed(() => Boolean(
  props.sessionId
  && debugThreadsState.sessionId === props.sessionId
  && debugThreadsState.loading,
));
const panelError = computed(() =>
  props.sessionId && debugThreadsState.sessionId === props.sessionId
    ? debugThreadsState.error
    : null,
);
const canContinueAll = computed(() => threads.value.some((thread) => thread.state === "stopped"));
const canPauseAll = computed(() => threads.value.some((thread) => thread.state !== "stopped"));

function threadName(thread: DebugThreadInfo): string {
  return thread.name || t("debugThreads.unnamedThread", { id: thread.id });
}

function threadStateLabel(state: DebugThreadState): string {
  if (state === "stopped") return t("debugThreads.stateStopped");
  if (state === "stepping") return t("debugThreads.stateStepping");
  return t("debugThreads.stateRunning");
}

function frameKey(threadId: number, frame: DebugStackFrame): string {
  return `${threadId}:${frame.id}`;
}

function frameLabel(frame: DebugStackFrame): string {
  return t("debugThreads.selectFrame", {
    name: frame.name || t("debugThreads.unnamedFrame"),
    file: frame.file || t("debugThreads.unknownSource"),
    line: frame.line,
  });
}

function refresh(): void {
  if (!props.sessionId) return;
  void listDebugThreads(props.sessionId);
}

function continueAll(): void {
  void continueAllDebugThreads(props.sessionId);
}

function pauseAll(): void {
  void pauseAllDebugThreads(props.sessionId);
}

async function selectThread(thread: DebugThreadInfo): Promise<void> {
  focusedThreadId.value = thread.id;
  if (await selectDebugThread(props.sessionId, thread.id)) {
    emit("selectThread", thread);
  }
}

function toggleThread(thread: DebugThreadInfo): void {
  void toggleDebugThreadExpanded(props.sessionId, thread.id);
}

function gotoFrame(frame: DebugStackFrame, thread: DebugThreadInfo): void {
  emit("goto-frame", frame, thread);
}

function focusThread(threadId: number): void {
  focusedThreadId.value = threadId;
  panel.value
    ?.querySelector<HTMLButtonElement>(`button[data-thread-id="${threadId}"]`)
    ?.focus();
}

function handleThreadKeydown(event: KeyboardEvent, threadId: number): void {
  if (!["ArrowUp", "ArrowDown", "Home", "End"].includes(event.key)) return;
  event.preventDefault();
  const items = threads.value;
  if (!items.length) return;
  const currentIndex = Math.max(0, items.findIndex((thread) => thread.id === threadId));
  let nextIndex = currentIndex;
  if (event.key === "ArrowUp") nextIndex = Math.max(0, currentIndex - 1);
  if (event.key === "ArrowDown") nextIndex = Math.min(items.length - 1, currentIndex + 1);
  if (event.key === "Home") nextIndex = 0;
  if (event.key === "End") nextIndex = items.length - 1;
  focusThread(items[nextIndex].id);
}

watch(
  () => [debugThreadsState.selected, ...threads.value.map((thread) => thread.id)],
  () => {
    if (focusedThreadId.value !== null
      && threads.value.some((thread) => thread.id === focusedThreadId.value)) {
      return;
    }
    focusedThreadId.value = debugThreadsState.selected ?? threads.value[0]?.id ?? null;
  },
  { immediate: true },
);

watch(
  () => props.sessionId,
  (sessionId) => {
    focusedThreadId.value = null;
    if (sessionId) void listDebugThreads(sessionId);
  },
  { immediate: true },
);

onMounted(() => {
  if (!props.autoActivate) return;
  releaseActivation = acquireDebugThreadsActivation();
});

onBeforeUnmount(() => {
  releaseActivation?.();
  releaseActivation = null;
});
</script>

<template>
  <section ref="panel" class="debug-threads" :aria-label="t('debugThreads.title')">
    <header class="debug-threads__header">
      <h3 class="debug-threads__title">{{ t("debugThreads.title") }}</h3>
      <div class="debug-threads__toolbar" role="toolbar" :aria-label="t('debugThreads.title')">
        <button
          type="button"
          class="debug-threads__icon-button"
          data-test="debug-threads-continue-all"
          :aria-label="t('debugThreads.continueAll')"
          :title="t('debugThreads.continueAll')"
          :disabled="!canContinueAll || debugThreadsState.bulkActionLoading"
          @click="continueAll"
        >
          <el-icon aria-hidden="true"><VideoPlay /></el-icon>
        </button>
        <button
          type="button"
          class="debug-threads__icon-button"
          data-test="debug-threads-pause-all"
          :aria-label="t('debugThreads.pauseAll')"
          :title="t('debugThreads.pauseAll')"
          :disabled="!canPauseAll || debugThreadsState.bulkActionLoading"
          @click="pauseAll"
        >
          <el-icon aria-hidden="true"><VideoPause /></el-icon>
        </button>
        <button
          type="button"
          class="debug-threads__icon-button"
          data-test="debug-threads-refresh"
          :aria-label="t('debugThreads.refresh')"
          :title="t('debugThreads.refresh')"
          :disabled="!props.sessionId || panelLoading || debugThreadsState.bulkActionLoading"
          @click="refresh"
        >
          <el-icon aria-hidden="true" :class="{ 'is-loading': panelLoading }"><Refresh /></el-icon>
        </button>
      </div>
    </header>

    <div v-if="panelError" class="debug-threads__error" role="alert">
      <span>{{ t("debugThreads.error") }}</span>
      <button
        type="button"
        class="debug-threads__retry"
        :aria-label="t('debugThreads.retry')"
        @click="refresh"
      >
        {{ t("debugThreads.retry") }}
      </button>
    </div>

    <div
      v-if="panelLoading && !threads.length"
      class="debug-threads__status"
      role="status"
      aria-live="polite"
    >
      {{ t("debugThreads.loading") }}
    </div>
    <div
      v-else-if="!threads.length"
      class="debug-threads__status"
      role="status"
      aria-live="polite"
    >
      {{ t("debugThreads.empty") }}
    </div>

    <ul
      v-else
      class="debug-threads__tree"
      :aria-label="t('debugThreads.list')"
      :aria-busy="panelLoading"
    >
      <li
        v-for="thread in threads"
        :key="thread.id"
        class="debug-threads__thread"
        :class="{ 'is-selected': thread.selected }"
      >
        <div class="debug-threads__row">
          <button
            type="button"
            class="debug-threads__icon-button debug-threads__expand"
            :class="{ 'is-expanded': debugThreadsState.expanded.has(thread.id) }"
            :aria-label="debugThreadsState.expanded.has(thread.id)
              ? t('debugThreads.collapseThread', { name: threadName(thread) })
              : t('debugThreads.expandThread', { name: threadName(thread) })"
            :aria-expanded="debugThreadsState.expanded.has(thread.id)"
            :disabled="thread.state !== 'stopped' && thread.frames.length === 0"
            @click="toggleThread(thread)"
          >
            <el-icon aria-hidden="true"><CaretRight /></el-icon>
          </button>

          <button
            type="button"
            class="debug-threads__select"
            data-thread-select
            :data-thread-id="thread.id"
            :tabindex="focusedThreadId === thread.id ? 0 : -1"
            :aria-current="thread.selected ? 'true' : undefined"
            :aria-label="t('debugThreads.selectThread', {
              name: threadName(thread),
              state: threadStateLabel(thread.state),
            })"
            @focus="focusedThreadId = thread.id"
            @click="selectThread(thread)"
            @keydown="handleThreadKeydown($event, thread.id)"
          >
            <el-icon
              class="debug-threads__state-icon"
              :class="`is-${thread.state}`"
              aria-hidden="true"
            >
              <VideoPause v-if="thread.state === 'stopped'" />
              <Loading v-else-if="thread.state === 'stepping'" />
              <VideoPlay v-else />
            </el-icon>
            <span class="debug-threads__identity">
              <span class="debug-threads__name">{{ threadName(thread) }}</span>
              <span class="debug-threads__state">{{ threadStateLabel(thread.state) }}</span>
            </span>
          </button>

          <div v-if="thread.selected" class="debug-threads__actions">
            <button
              type="button"
              class="debug-threads__icon-button"
              data-test="debug-thread-continue"
              :aria-label="t('debugThreads.continueThread', { name: threadName(thread) })"
              :title="t('debugThreads.continueThread', { name: threadName(thread) })"
              :disabled="thread.state !== 'stopped'
                || debugThreadsState.bulkActionLoading
                || debugThreadsState.actionLoading.has(thread.id)"
              @click.stop="continueDebugThread(props.sessionId, thread.id)"
            >
              <el-icon aria-hidden="true"><VideoPlay /></el-icon>
            </button>
            <button
              type="button"
              class="debug-threads__icon-button"
              data-test="debug-thread-step-over"
              :aria-label="t('debugThreads.stepOverThread', { name: threadName(thread) })"
              :title="t('debugThreads.stepOverThread', { name: threadName(thread) })"
              :disabled="thread.state !== 'stopped'
                || debugThreadsState.bulkActionLoading
                || debugThreadsState.actionLoading.has(thread.id)"
              @click.stop="stepDebugThread(props.sessionId, thread.id, 'next')"
            >
              <el-icon aria-hidden="true"><RefreshRight /></el-icon>
            </button>
            <button
              type="button"
              class="debug-threads__icon-button"
              data-test="debug-thread-step-in"
              :aria-label="t('debugThreads.stepInThread', { name: threadName(thread) })"
              :title="t('debugThreads.stepInThread', { name: threadName(thread) })"
              :disabled="thread.state !== 'stopped'
                || debugThreadsState.bulkActionLoading
                || debugThreadsState.actionLoading.has(thread.id)"
              @click.stop="stepDebugThread(props.sessionId, thread.id, 'in')"
            >
              <el-icon aria-hidden="true"><BottomRight /></el-icon>
            </button>
            <button
              type="button"
              class="debug-threads__icon-button"
              data-test="debug-thread-step-out"
              :aria-label="t('debugThreads.stepOutThread', { name: threadName(thread) })"
              :title="t('debugThreads.stepOutThread', { name: threadName(thread) })"
              :disabled="thread.state !== 'stopped'
                || debugThreadsState.bulkActionLoading
                || debugThreadsState.actionLoading.has(thread.id)"
              @click.stop="stepDebugThread(props.sessionId, thread.id, 'out')"
            >
              <el-icon aria-hidden="true"><TopRight /></el-icon>
            </button>
          </div>
        </div>

        <div
          v-if="debugThreadsState.expanded.has(thread.id)"
          class="debug-threads__frames"
        >
          <div
            v-if="debugThreadsState.loadingStacks.has(thread.id)"
            class="debug-threads__frame-status"
            role="status"
          >
            {{ t("debugThreads.loadingStack") }}
          </div>
          <ul
            v-else-if="thread.frames.length"
            class="debug-threads__frame-list"
            :aria-label="t('debugThreads.stackForThread', { name: threadName(thread) })"
          >
            <li
              v-for="frame in thread.frames"
              :key="frameKey(thread.id, frame)"
              class="debug-threads__frame-item"
            >
              <button
                type="button"
                class="debug-threads__frame"
                :class="{ 'is-subtle': frame.presentationHint === 'subtle' }"
                :aria-label="frameLabel(frame)"
                @click="emit('selectFrame', frame, thread)"
                @dblclick="gotoFrame(frame, thread)"
                @keydown.enter.prevent="gotoFrame(frame, thread)"
                @keydown.space.prevent="gotoFrame(frame, thread)"
              >
                <span class="debug-threads__frame-name">
                  {{ frame.name || t("debugThreads.unnamedFrame") }}
                </span>
                <span class="debug-threads__frame-location">
                  {{ frame.file || t("debugThreads.unknownSource") }}:{{ frame.line }}
                </span>
              </button>
            </li>
          </ul>
          <div v-else class="debug-threads__frame-status" role="status">
            {{ t("debugThreads.noFrames") }}
          </div>
        </div>
      </li>
    </ul>
  </section>
</template>

<style scoped>
.debug-threads {
  display: flex;
  min-height: 0;
  flex-direction: column;
  color: var(--color-text-primary, #e7e7e7);
  background: var(--color-bg-panel, transparent);
  font-size: 12px;
}

.debug-threads__header {
  display: flex;
  min-height: 34px;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 4px 6px 4px 10px;
  border-bottom: 1px solid var(--color-border, rgba(255, 255, 255, 0.1));
}

.debug-threads__title {
  min-width: 0;
  margin: 0;
  overflow: hidden;
  font-size: 12px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.debug-threads__toolbar {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 1px;
}

.debug-threads__icon-button {
  display: inline-flex;
  width: 26px;
  height: 26px;
  flex: 0 0 26px;
  align-items: center;
  justify-content: center;
  padding: 0;
  border: 0;
  border-radius: 3px;
  color: inherit;
  background: transparent;
  cursor: pointer;
}

.debug-threads__icon-button:hover:not(:disabled),
.debug-threads__icon-button:focus-visible,
.debug-threads__select:hover,
.debug-threads__select:focus-visible,
.debug-threads__frame:hover,
.debug-threads__frame:focus-visible {
  background: var(--color-bg-surface-container-low, rgba(255, 255, 255, 0.08));
  outline: none;
}

.debug-threads__icon-button:focus-visible,
.debug-threads__select:focus-visible,
.debug-threads__frame:focus-visible,
.debug-threads__retry:focus-visible {
  box-shadow: inset 0 0 0 2px var(--color-primary-focus, #75a7ff);
}

.debug-threads__icon-button:disabled {
  cursor: default;
  opacity: 0.42;
}

.debug-threads__error {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 7px 10px;
  color: var(--color-error, #ff7b72);
  background: color-mix(in srgb, var(--color-error, #ff7b72) 10%, transparent);
}

.debug-threads__retry {
  min-height: 24px;
  padding: 2px 8px;
  border: 1px solid currentColor;
  border-radius: 3px;
  color: inherit;
  background: transparent;
  cursor: pointer;
}

.debug-threads__status {
  padding: 16px 10px;
  color: var(--color-text-secondary, #aaa);
  text-align: center;
}

.debug-threads__tree,
.debug-threads__frame-list {
  margin: 0;
  padding: 0;
  list-style: none;
}

.debug-threads__tree {
  min-height: 0;
  flex: 1;
  overflow: auto;
}

.debug-threads__thread {
  border-bottom: 1px solid var(--color-border, rgba(255, 255, 255, 0.06));
}

.debug-threads__thread.is-selected {
  background: var(--color-bg-selected, rgba(82, 139, 255, 0.12));
}

.debug-threads__row {
  display: flex;
  min-height: 38px;
  align-items: center;
  gap: 2px;
  padding: 3px 5px;
}

.debug-threads__expand {
  transition: transform 120ms ease;
}

.debug-threads__expand.is-expanded {
  transform: rotate(90deg);
}

.debug-threads__select {
  display: flex;
  min-width: 0;
  min-height: 30px;
  flex: 1;
  align-items: center;
  gap: 7px;
  padding: 3px 5px;
  border: 0;
  border-radius: 3px;
  color: inherit;
  background: transparent;
  text-align: left;
  cursor: pointer;
}

.debug-threads__state-icon {
  width: 16px;
  height: 16px;
  flex: 0 0 16px;
}

.debug-threads__state-icon.is-running {
  color: var(--color-success, #4ec86f);
}

.debug-threads__state-icon.is-stopped {
  color: var(--color-error, #ff6b6b);
}

.debug-threads__state-icon.is-stepping {
  color: var(--color-warning, #e7b34c);
  animation: debug-threads-spin 900ms linear infinite;
}

.debug-threads__identity {
  display: flex;
  min-width: 0;
  flex: 1;
  flex-direction: column;
  gap: 1px;
}

.debug-threads__name,
.debug-threads__state,
.debug-threads__frame-name,
.debug-threads__frame-location {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.debug-threads__name {
  font-weight: 600;
}

.debug-threads__state {
  color: var(--color-text-secondary, #aaa);
  font-size: 10px;
}

.debug-threads__actions {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 1px;
}

.debug-threads__frames {
  padding: 0 5px 5px 35px;
}

.debug-threads__frame-item {
  min-width: 0;
}

.debug-threads__frame {
  display: flex;
  width: 100%;
  min-height: 34px;
  flex-direction: column;
  justify-content: center;
  gap: 1px;
  padding: 4px 7px;
  border: 0;
  border-radius: 3px;
  color: inherit;
  background: transparent;
  text-align: left;
  cursor: pointer;
}

.debug-threads__frame.is-subtle {
  opacity: 0.62;
}

.debug-threads__frame-location {
  color: var(--color-text-secondary, #aaa);
  font-family: var(--font-mono, monospace);
  font-size: 10px;
}

.debug-threads__frame-status {
  min-height: 30px;
  padding: 7px;
  color: var(--color-text-secondary, #aaa);
}

.is-loading {
  animation: debug-threads-spin 900ms linear infinite;
}

@keyframes debug-threads-spin {
  to {
    transform: rotate(360deg);
  }
}

@media (prefers-reduced-motion: reduce) {
  .debug-threads__expand {
    transition: none;
  }

  .debug-threads__state-icon.is-stepping,
  .is-loading {
    animation: none;
  }
}
</style>
