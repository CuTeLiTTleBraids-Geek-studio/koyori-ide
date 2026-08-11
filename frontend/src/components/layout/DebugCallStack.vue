<script setup lang="ts">
// Koyori IDE 组件 · Debug Call Stack。
// 喵，这是 Debug Call Stack，负责 Koyori IDE 的界面呈现喵~
import { computed } from "vue";
import { RefreshLeft } from "@element-plus/icons-vue";
import {
  debugState,
  loadAsyncParentStack,
  loadMoreStackFrames,
  type DebugStackFrame,
} from "@/stores/debug";
import { useI18n } from "@/lib/i18n";

const { t } = useI18n();
const frameKeys = new WeakMap<DebugStackFrame, string>();
let frameKeySequence = 0;

function frameKey(frame: DebugStackFrame): string {
  const existing = frameKeys.get(frame);
  if (existing) return existing;
  const key = `debug-frame-${++frameKeySequence}`;
  frameKeys.set(frame, key);
  return key;
}

const emit = defineEmits<{
  selectFrame: [file: string, line: number, frameId: number];
  restartFrame: [frameId: number];
}>();

const nextAsyncParentId = computed(() => {
  const segments = debugState.asyncStackSegments;
  return segments.length
    ? segments[segments.length - 1]?.parentId || ""
    : debugState.asyncStackRootId;
});

function selectFrame(frame: DebugStackFrame): void {
  if (frame.asyncBoundary) return;
  emit("selectFrame", frame.file, frame.line, frame.id);
}
</script>

<template>
  <div class="debug-call-stack">
    <ul v-if="debugState.stack.length" class="debug-call-stack__list" role="tree">
      <template v-for="frame in debugState.stack" :key="frameKey(frame)">
        <li
          v-if="frame.asyncBoundary"
          class="debug-call-stack__boundary"
          data-testid="async-boundary"
        >
          <span class="debug-call-stack__boundary-line" aria-hidden="true"></span>
          <span>{{ frame.name }}</span>
        </li>
        <li
          v-else
          class="debug-call-stack__frame"
          :class="{ 'debug-call-stack__frame--subtle': frame.presentationHint === 'subtle' }"
        >
          <button
            type="button"
            class="debug-call-stack__select"
            role="treeitem"
            :aria-label="`${frame.name}${frame.file ? `, ${frame.file}:${frame.line}` : ''}`"
            @click="selectFrame(frame)"
            @keydown.enter.prevent="selectFrame(frame)"
            @keydown.space.prevent="selectFrame(frame)"
          >
            <span class="debug-call-stack__name">{{ frame.name }}</span>
            <span v-if="frame.file" class="debug-call-stack__loc">{{ frame.file }}:{{ frame.line }}</span>
          </button>
          <button
            v-if="frame.id > 0"
            type="button"
            class="debug-call-stack__restart"
            :disabled="!debugState.running || !debugState.stopped"
            :title="t('debug.asyncStack.restartFrame')"
            :aria-label="t('debug.asyncStack.restartFrame')"
            @click.stop="emit('restartFrame', frame.id)"
          >
            <el-icon :size="14"><RefreshLeft /></el-icon>
          </button>
        </li>
      </template>
    </ul>
    <p v-else class="debug-call-stack__empty">{{ t("debug.asyncStack.noFrames") }}</p>

    <button
      v-if="debugState.supportsDelayedStackTraceLoading && debugState.stackHasMore"
      type="button"
      class="debug-call-stack__load"
      data-testid="load-more-stack"
      :disabled="debugState.stackPageLoading || !debugState.running || !debugState.stopped"
      @click="loadMoreStackFrames()"
    >
      {{ debugState.stackPageLoading ? t("debug.asyncStack.loading") : t("debug.asyncStack.loadMore") }}
    </button>

    <div v-if="debugState.supportsAsyncStackTrace" class="debug-call-stack__async">
      <section
        v-for="segment in debugState.asyncStackSegments"
        :key="segment.id"
        class="debug-call-stack__segment"
      >
        <div class="debug-call-stack__boundary" data-testid="async-segment-boundary">
          <span class="debug-call-stack__boundary-line" aria-hidden="true"></span>
          <span>{{ segment.description || t("debug.asyncStack.asyncCall") }}</span>
        </div>
        <button
          v-for="frame in segment.frames"
          :key="frameKey(frame)"
          type="button"
          class="debug-call-stack__frame debug-call-stack__frame--async"
          @click="emit('selectFrame', frame.file, frame.line, 0)"
        >
          <span class="debug-call-stack__name">{{ frame.name }}</span>
          <span v-if="frame.file" class="debug-call-stack__loc">{{ frame.file }}:{{ frame.line }}</span>
        </button>
      </section>
      <button
        v-if="nextAsyncParentId"
        type="button"
        class="debug-call-stack__load"
        data-testid="load-async-parent"
        :disabled="debugState.asyncStackLoading || !debugState.running || !debugState.stopped"
        @click="loadAsyncParentStack(nextAsyncParentId)"
      >
        {{ debugState.asyncStackLoading ? t("debug.asyncStack.loading") : t("debug.asyncStack.loadParent") }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.debug-call-stack__list {
  list-style: none;
  margin: 0;
  padding: 0;
}

.debug-call-stack__frame {
  position: relative;
  width: 100%;
  min-height: 36px;
  border-radius: 3px;
}

.debug-call-stack__frame:hover {
  background: rgba(255, 255, 255, 0.06);
}

.debug-call-stack__select {
  display: flex;
  width: 100%;
  min-height: 36px;
  flex-direction: column;
  justify-content: center;
  padding: 4px 30px 4px 2px;
  border: 0;
  color: inherit;
  background: transparent;
  text-align: left;
  cursor: pointer;
}

.debug-call-stack__select:focus-visible,
.debug-call-stack__restart:focus-visible,
.debug-call-stack__load:focus-visible {
  outline: 2px solid var(--color-primary-focus);
  outline-offset: -2px;
}

.debug-call-stack__frame--subtle {
  opacity: 0.62;
}

.debug-call-stack__frame--async {
  padding-left: 14px;
}

.debug-call-stack__name {
  color: var(--color-text-primary, #eee);
  font-weight: 500;
}

.debug-call-stack__loc {
  overflow-wrap: anywhere;
  opacity: 0.72;
}

.debug-call-stack__restart {
  position: absolute;
  top: 7px;
  right: 3px;
  width: 24px;
  height: 24px;
  border: 0;
  color: inherit;
  background: transparent;
  cursor: pointer;
}

.debug-call-stack__boundary {
  display: flex;
  min-height: 28px;
  align-items: center;
  gap: 7px;
  color: var(--color-text-secondary, #aab2bf);
  font-size: 11px;
}

.debug-call-stack__boundary-line {
  width: 18px;
  border-top: 1px dashed currentColor;
  opacity: 0.7;
}

.debug-call-stack__async {
  margin-top: 4px;
}

.debug-call-stack__load {
  min-height: 26px;
  margin-top: 4px;
  padding: 3px 6px;
  border: 1px solid var(--color-border, #3b424d);
  border-radius: 3px;
  color: inherit;
  background: transparent;
  cursor: pointer;
}

.debug-call-stack__load:disabled {
  cursor: default;
  opacity: 0.55;
}

.debug-call-stack__empty {
  margin: 0;
  opacity: 0.5;
}
</style>
