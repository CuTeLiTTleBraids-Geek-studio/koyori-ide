<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from "vue";
import { useI18n } from "@/lib/i18n";
import { agentTimelineState, type AgentTimelineEntry } from "@/stores/agentTimeline";

const { t } = useI18n();
const entries = computed(() => agentTimelineState.entries);
const sectionElement = ref<HTMLElement | null>(null);
const followsLatest = ref(true);
const FOLLOW_THRESHOLD_PX = 32;

function isNearBottom(element: HTMLElement): boolean {
  return element.scrollHeight - element.clientHeight - element.scrollTop <= FOLLOW_THRESHOLD_PX;
}

function onScroll(): void {
  const element = sectionElement.value;
  if (element) followsLatest.value = isNearBottom(element);
}

function scrollToLatest(force = false): void {
  const element = sectionElement.value;
  if (!element || (!force && !followsLatest.value)) return;
  element.scrollTop = element.scrollHeight;
}

// A lifecycle transition can update an existing entry rather than append one.
// Include visible fields so approval/executing/result changes stay in view.
const entryFingerprint = computed(() => entries.value.map((entry) => [
  entry.id,
  entry.stage,
  entry.status ?? "",
  entry.updatedAt,
  entry.detail ?? "",
].join("\u0000")).join("\u0001"));

// Tool and reasoning entries arrive independently of message chunks. Keep
// the activity feed pinned to the newest entry so a live run remains visible
// in both the embedded panel and the standalone AI window.
watch(
  entryFingerprint,
  () => {
    void nextTick(() => {
      scrollToLatest();
    });
  },
  { flush: "post" },
);

// A completed turn clears the entries while keeping this component mounted.
// Treat the next run as a fresh feed so a prior manual scroll position cannot
// leave the new execution hidden at the top of the timeline.
watch(
  () => entries.value.length === 0,
  (empty) => {
    if (empty) followsLatest.value = true;
  },
);

onMounted(() => {
  // The first entry may already exist when the component mounts; wait for its
  // layout before establishing the initial follow position.
  void nextTick(() => scrollToLatest(true));
});


function label(entry: AgentTimelineEntry): string {
  if (entry.stage === "requested") return t("aiChat.timeline.tool-requested");
  if (entry.stage === "approval") {
    if (entry.status === "approved") return t("aiChat.timeline.approved");
    if (entry.status === "waiting-approval") return t("aiChat.timeline.waiting-approval");
  }
  if (entry.stage === "result" && entry.status === "rejected") return t("aiChat.timeline.rejected");
  if (entry.stage === "result" && entry.status === "error") return t("aiChat.timeline.error");
  if (entry.stage === "result" && entry.status === "executed") return t("aiChat.timeline.executed");
  if (entry.stage === "result") return t("aiChat.timeline.result");
  return t(`aiChat.timeline.${entry.stage}`);
}

function stageClass(entry: AgentTimelineEntry): string {
  return `agent-timeline__entry--${entry.stage}`;
}

function formatTime(timestamp: number): string {
  return new Date(timestamp).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}
</script>

<template>
  <section ref="sectionElement" v-if="entries.length > 0" class="agent-timeline" aria-live="polite" :aria-label="t('aiChat.timeline.title')" @scroll.passive="onScroll">
    <div class="agent-timeline__heading">
      <span class="agent-timeline__title">{{ t("aiChat.timeline.title") }}</span>
      <span class="agent-timeline__hint">{{ t("aiChat.timeline.hint") }}</span>
    </div>
    <ol class="agent-timeline__list">
      <li v-for="entry in entries" :key="entry.id" class="agent-timeline__entry" :class="stageClass(entry)">
        <span class="agent-timeline__dot" aria-hidden="true" />
        <div class="agent-timeline__body">
          <div class="agent-timeline__meta">
            <strong>{{ label(entry) }}</strong>
            <span v-if="entry.tool" class="agent-timeline__tool">{{ entry.tool }}</span>
            <time :datetime="new Date(entry.createdAt).toISOString()">{{ formatTime(entry.createdAt) }}</time>
          </div>
          <span v-if="entry.target" class="agent-timeline__target">{{ entry.target }}</span>
          <code v-if="entry.detail" class="agent-timeline__detail">{{ entry.detail }}</code>
        </div>
      </li>
    </ol>
  </section>
</template>

<style scoped>
.agent-timeline {
  flex: 0 0 auto;
  max-height: 196px;
  overflow: auto;
  padding: 8px 12px;
  border-top: 1px solid var(--color-border-default, #d9d9df);
  color: var(--color-text-secondary, #6b7280);
  background: var(--color-bg-surface-container-low, rgba(127, 127, 127, 0.06));
}
.agent-timeline__heading {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 6px;
}
.agent-timeline__title { color: var(--color-text-primary, #202124); font-size: 11px; font-weight: 650; }
.agent-timeline__hint { color: var(--color-text-tertiary, #8a8f98); font-size: 10px; }
.agent-timeline__list { display: grid; gap: 5px; margin: 0; padding: 0; list-style: none; }
.agent-timeline__entry { display: grid; grid-template-columns: 9px minmax(0, 1fr); gap: 7px; align-items: start; min-width: 0; }
.agent-timeline__dot { width: 7px; height: 7px; margin-top: 4px; border-radius: 50%; background: var(--color-primary, #4f7cff); box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-primary, #4f7cff) 15%, transparent); }
.agent-timeline__entry--result .agent-timeline__dot { background: var(--color-success, #2f9e68); }
.agent-timeline__entry--observation .agent-timeline__dot { background: var(--color-success, #2f9e68); }
.agent-timeline__entry--approval .agent-timeline__dot { background: var(--color-warning, #c58a18); }
.agent-timeline__entry--reasoning .agent-timeline__dot { background: var(--color-text-tertiary, #8a8f98); }
.agent-timeline__body { min-width: 0; }
.agent-timeline__meta { display: flex; align-items: baseline; gap: 6px; min-width: 0; font-size: 10px; }
.agent-timeline__meta strong { color: var(--color-text-primary, #202124); font-weight: 600; }
.agent-timeline__tool { overflow: hidden; color: var(--color-primary, #4f7cff); text-overflow: ellipsis; white-space: nowrap; }
.agent-timeline__target { overflow: hidden; color: var(--color-text-secondary, #6b7280); text-overflow: ellipsis; white-space: nowrap; }
.agent-timeline__meta time { margin-left: auto; color: var(--color-text-tertiary, #8a8f98); font-variant-numeric: tabular-nums; }
.agent-timeline__detail { display: block; max-width: 100%; overflow: hidden; color: var(--color-text-secondary, #6b7280); font: inherit; text-overflow: ellipsis; white-space: pre-wrap; }
@media (prefers-reduced-motion: reduce) { .agent-timeline { scroll-behavior: auto; } }
</style>
