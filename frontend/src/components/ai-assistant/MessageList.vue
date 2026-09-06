<script setup lang="ts">
// Koyori IDE 组件 · Message List。
// 喵，这是 Message List，负责 Koyori IDE 的界面呈现喵~
// Plan 11 Task 1 — 中间消息列表骨架。复用 aiState（与嵌入式 AiChatPanel
// 共享同一 reactive 实例，切换不丢失）。Markdown 渲染走 MarkdownContent
// SFC + renderMarkdown（内部 DOMPurify 净化），禁 v-html（G-SEC-11）。
// Task 2/3 充实滚动/流式/工具调用展示。
// Task 15 Step 7: 气泡样式由 personalization.bubbleStyle 驱动（rounded/sharp/bubble）。
//
// M-25 / prompt-4 D9: dependency-free dynamic-height virtualization.
// ResizeObserver records each rendered row's real height. Prefix offsets are
// then recomputed from the cache, so long Markdown messages do not make the
// scroll window drift away from the actual content.
import {
  computed,
  nextTick,
  onBeforeUnmount,
  onMounted,
  reactive,
  ref,
  watch,
  type ComponentPublicInstance,
} from "vue";
import { useI18n } from "@/lib/i18n";
import { aiState } from "@/stores/ai";
import { appState } from "@/stores/app";
import { extractToolCallBlocks, isAgentMode } from "@/stores/agent";
import { renderMarkdown } from "@/lib/markdown";
import MarkdownContent from "@/components/common/MarkdownContent.vue";
import AgentExecutionTimeline from "@/components/ai-assistant/AgentExecutionTimeline.vue";
import AgentToolCalls from "@/components/ai-assistant/AgentToolCalls.vue";

const { t } = useI18n();

const bubbleClass = computed(() => {
  const style = appState.personalization.bubbleStyle ?? "rounded";
  return `ai-msg--${style}`;
});

const DEFAULT_ITEM_HEIGHT = 96;
const MIN_ITEM_HEIGHT = 36;
const ITEM_GAP = 12;
const OVERSCAN_PX = 600;
const FOLLOW_TAIL_THRESHOLD_PX = 96;

const listElement = ref<HTMLElement | null>(null);
const activityElement = ref<HTMLElement | null>(null);
const scrollTop = ref(0);
const viewportHeight = ref(600);
const messageHeights = reactive(new Map<string, number>());
const messageElements = new Map<string, HTMLElement>();
let resizeObserver: ResizeObserver | null = null;
let shouldFollowTail = true;
let followTailScheduled = false;

function isNearTail(element: HTMLElement): boolean {
  const distance = element.scrollHeight - element.clientHeight - element.scrollTop;
  return distance <= FOLLOW_TAIL_THRESHOLD_PX;
}

function scheduleFollowTail(): void {
  if (!shouldFollowTail || followTailScheduled) return;
  followTailScheduled = true;
  void nextTick(() => {
    followTailScheduled = false;
    const element = listElement.value;
    if (!element || !shouldFollowTail) return;
    const nextScrollTop = Math.max(0, element.scrollHeight - element.clientHeight);
    element.scrollTop = nextScrollTop;
    scrollTop.value = nextScrollTop;
  });
}

function onScroll(e: Event): void {
  const el = e.target as HTMLElement;
  scrollTop.value = el.scrollTop;
  shouldFollowTail = isNearTail(el);
  // jsdom 中 clientHeight 为 0，保留默认值避免渲染全量
  if (el.clientHeight > 0) {
    viewportHeight.value = el.clientHeight;
  }
}

const messageLayout = computed(() => {
  const offsets = new Array<number>(aiState.messages.length + 1);
  offsets[0] = 0;
  for (let index = 0; index < aiState.messages.length; index += 1) {
    const message = aiState.messages[index];
    const height = messageHeights.get(message.id) ?? DEFAULT_ITEM_HEIGHT;
    offsets[index + 1] = offsets[index] + height;
    if (index < aiState.messages.length - 1) offsets[index + 1] += ITEM_GAP;
  }
  return {
    offsets,
    totalHeight: offsets[aiState.messages.length] ?? 0,
  };
});

function indexAtOffset(offset: number): number {
  const total = aiState.messages.length;
  if (total === 0) return 0;
  const { offsets } = messageLayout.value;
  let low = 0;
  let high = total;
  while (low < high) {
    const middle = Math.floor((low + high) / 2);
    if (offsets[middle + 1] <= offset) low = middle + 1;
    else high = middle;
  }
  return Math.min(low, total - 1);
}

const visibleRange = computed(() => {
  const total = aiState.messages.length;
  if (total === 0) return { startIdx: 0, endIdx: 0 };
  const startOffset = Math.max(0, scrollTop.value - OVERSCAN_PX);
  const endOffset = scrollTop.value + viewportHeight.value + OVERSCAN_PX;
  return {
    startIdx: indexAtOffset(startOffset),
    endIdx: Math.min(total, indexAtOffset(endOffset) + 1),
  };
});

const visibleMessages = computed(() => {
  const { startIdx, endIdx } = visibleRange.value;
  return aiState.messages.slice(startIdx, endIdx).map((msg, offset) => {
    const actualIndex = startIdx + offset;
    return {
      msg,
      actualIndex,
      top: messageLayout.value.offsets[actualIndex],
    };
  });
});

function toHTMLElement(
  value: Element | ComponentPublicInstance | null,
): HTMLElement | null {
  if (value instanceof HTMLElement) return value;
  if (!value || value instanceof Element) return null;
  const componentElement = value.$el;
  return componentElement instanceof HTMLElement ? componentElement : null;
}

function setMessageElement(
  value: Element | ComponentPublicInstance | null,
  messageId: string,
): void {
  const previous = messageElements.get(messageId);
  const element = toHTMLElement(value);
  if (previous && previous !== element) resizeObserver?.unobserve(previous);
  if (!element) {
    messageElements.delete(messageId);
    return;
  }

  messageElements.set(messageId, element);
  resizeObserver?.observe(element);
  if (!resizeObserver) {
    void nextTick(() => {
      const height = element.getBoundingClientRect().height;
      if (height > 0) recordMessageHeight(messageId, height);
    });
  }
}

function recordMessageHeight(messageId: string, rawHeight: number): void {
  if (!Number.isFinite(rawHeight) || rawHeight <= 0) return;
  const height = Math.max(MIN_ITEM_HEIGHT, Math.ceil(rawHeight));
  const previous = messageHeights.get(messageId) ?? DEFAULT_ITEM_HEIGHT;
  if (Math.abs(previous - height) < 1) return;

  const messageIndex = aiState.messages.findIndex((message) => message.id === messageId);
  // Rows above the viewport can still be mounted because of overscan. Anchor
  // against the first row intersecting the real viewport, not the overscan
  // start, otherwise a late Markdown resize shifts the user's reading point.
  const viewportAnchorIndex = indexAtOffset(scrollTop.value);
  const keepScrollAnchor = messageIndex >= 0 && messageIndex < viewportAnchorIndex;
  messageHeights.set(messageId, height);

  if (keepScrollAnchor && listElement.value) {
    const delta = height - previous;
    listElement.value.scrollTop += delta;
    scrollTop.value = listElement.value.scrollTop;
  }
  scheduleFollowTail();
}

function onResize(entries: ResizeObserverEntry[]): void {
  for (const entry of entries) {
    if (entry.target === listElement.value) {
      const height = entry.contentRect.height || listElement.value?.clientHeight || 0;
      if (height > 0) viewportHeight.value = height;
      scheduleFollowTail();
      continue;
    }
    if (entry.target === activityElement.value) {
      scheduleFollowTail();
      continue;
    }
    const element = entry.target as HTMLElement;
    const messageId = element.dataset.messageId;
    if (messageId) recordMessageHeight(messageId, entry.contentRect.height);
  }
}

function messageContent(
  role: string,
  content: string,
  hasNativeToolCalls: boolean,
): string {
  if (role !== "assistant" || !isAgentMode.value || hasNativeToolCalls) return content;
  return extractToolCallBlocks(content).cleanedMessage;
}

function boundedToolArguments(value: string): string {
  const text = value.trim();
  return text.length > 480 ? `${text.slice(0, 480)}\n...` : text;
}

function boundedToolResult(value: string): string {
  return value.length > 640 ? `${value.slice(0, 640)}\n...` : value;
}

watch(
  () => aiState.messages.map((message) => message.id),
  (ids) => {
    const activeIds = new Set(ids);
    for (const id of messageHeights.keys()) {
      if (!activeIds.has(id)) messageHeights.delete(id);
    }
  },
  { flush: "post" },
);

watch(
  () => {
    const tail = aiState.messages[aiState.messages.length - 1];
    return [aiState.messages.length, tail?.id ?? "", tail?.content ?? ""] as const;
  },
  scheduleFollowTail,
  { flush: "post" },
);

onMounted(() => {
  const element = listElement.value;
  if (element?.clientHeight) viewportHeight.value = element.clientHeight;
  scheduleFollowTail();
  if (typeof ResizeObserver === "undefined") return;
  resizeObserver = new ResizeObserver(onResize);
  if (element) resizeObserver.observe(element);
  if (activityElement.value) resizeObserver.observe(activityElement.value);
  for (const messageElement of messageElements.values()) {
    resizeObserver.observe(messageElement);
  }
});

onBeforeUnmount(() => {
  resizeObserver?.disconnect();
  resizeObserver = null;
  messageElements.clear();
  messageHeights.clear();
});
</script>

<template>
  <div
    ref="listElement"
    class="ai-msg-list"
    role="log"
    aria-live="polite"
    @scroll.passive="onScroll"
  >
    <div v-if="aiState.messages.length === 0" class="ai-msg-list__empty">
      {{ t("aiAssistant.emptyHint") }}
    </div>
    <div
      v-else
      class="ai-msg-list__virtual"
      :style="{ height: `${messageLayout.totalHeight}px` }"
    >
      <div
        v-for="{ msg, actualIndex, top } in visibleMessages"
        :key="msg.id"
        :ref="(element) => setMessageElement(element, msg.id)"
        class="ai-msg-list__item"
        :class="`ai-msg-list__item--${msg.role}`"
        :data-message-id="msg.id"
        :data-message-index="actualIndex"
        :style="{ transform: `translateY(${top}px)` }"
      >
        <div class="ai-msg" :class="[`ai-msg--${msg.role}`, bubbleClass]">
          <div class="ai-msg__role">{{ msg.role }}</div>
          <MarkdownContent
            v-if="msg.role !== 'user' && (msg.content || !(msg.toolCalls?.length || msg.toolResults?.length))"
            class="ai-msg__body markdown-body"
            :html="renderMarkdown(messageContent(msg.role, msg.content, (msg.toolCalls?.length ?? 0) > 0))"
          />
          <div v-else-if="msg.role === 'user'" class="ai-msg__body">{{ msg.content }}</div>
          <div v-if="msg.toolCalls?.length" class="ai-msg__tools" data-testid="message-tool-calls">
            <div v-for="call in msg.toolCalls" :key="call.id" class="ai-msg__tool">
              <strong>{{ call.name }}</strong>
              <code>{{ boundedToolArguments(call.arguments) }}</code>
            </div>
          </div>
          <div v-if="msg.toolResults?.length" class="ai-msg__tools" data-testid="message-tool-results">
            <div v-for="result in msg.toolResults" :key="result.toolCallId" class="ai-msg__tool" :class="{ 'ai-msg__tool--error': result.isError }">
              <strong>{{ result.isError ? 'Tool error' : 'Tool result' }}</strong>
              <pre>{{ boundedToolResult(result.content) }}</pre>
            </div>
          </div>
        </div>
      </div>
    </div>
    <div ref="activityElement" class="ai-msg-list__activity">
      <AgentToolCalls />
      <AgentExecutionTimeline />
    </div>
  </div>
</template>

<style scoped>
.ai-msg-list {
  flex: 1;
  overflow-y: auto;
  padding: 16px;
  box-sizing: border-box;
}
.ai-msg-list__empty {
  display: grid;
  min-height: 100%;
  place-items: center;
  color: var(--color-text-secondary, #888);
  font-size: 13px;
}
.ai-msg-list__virtual {
  position: relative;
  width: 100%;
}
.ai-msg-list__activity {
  width: 100%;
}
.ai-msg-list__item {
  position: absolute;
  top: 0;
  left: 0;
  display: flex;
  width: 100%;
  will-change: transform;
}
.ai-msg-list__item--user {
  justify-content: flex-end;
}
.ai-msg {
  padding: 10px 12px;
  border-radius: 8px;
  max-width: 80%;
  box-sizing: border-box;
}
.ai-msg--user {
  background: var(--color-accent, #3b82f6);
  color: #fff;
}
.ai-msg--assistant {
  background: var(--color-bg-elevated, #f5f5f7);
}
.ai-msg__role {
  font-size: 11px;
  opacity: 0.7;
  margin-bottom: 4px;
  text-transform: uppercase;
}
.ai-msg__tools {
  display: grid;
  gap: 6px;
  margin-top: 8px;
  font-size: 12px;
}
.ai-msg__tool {
  display: grid;
  gap: 4px;
  padding: 7px 8px;
  border-left: 2px solid var(--color-accent, #3b82f6);
  background: color-mix(in srgb, var(--color-bg, #fff) 82%, var(--color-accent, #3b82f6));
}
.ai-msg__tool--error { border-left-color: var(--color-danger, #dc2626); }
.ai-msg__tool code, .ai-msg__tool pre { margin: 0; white-space: pre-wrap; overflow-wrap: anywhere; }
</style>
