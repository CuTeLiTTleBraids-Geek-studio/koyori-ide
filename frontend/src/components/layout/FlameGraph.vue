<script setup lang="ts">
// Koyori IDE 组件 · Flame Graph。
// 喵，这是 Flame Graph，负责 Koyori IDE 的界面呈现喵~
import { computed, nextTick, ref, watch } from "vue";
import { RefreshLeft } from "@element-plus/icons-vue";
import { findFlameNode, layoutFlameGraph, type FlameGraphFrame } from "@/lib/flameGraph";
import type { FlameGraphNode } from "@/types";
import { useI18n } from "@/lib/i18n";

const { t } = useI18n();

const props = defineProps<{
  root: FlameGraphNode | null;
  unit: string;
}>();

const query = ref("");
const zoomID = ref("");
const graphRoot = computed(() => {
  if (!props.root) return null;
  return zoomID.value ? findFlameNode(props.root, zoomID.value) ?? props.root : props.root;
});
const layout = computed(() => graphRoot.value ? layoutFlameGraph(graphRoot.value) : null);
// BUG2: the backend can serialize a nil children slice as `null`, and
// frames keep the original node, so a frame may have `node.children === null`.
// Guard every children.length read instead of crashing the view.
function hasChildren(frame: FlameGraphFrame): boolean {
  return (frame.node.children?.length ?? 0) > 0;
}
const zoomableFrames = computed(() => layout.value?.frames.filter(hasChildren) ?? []);
const normalizedQuery = computed(() => query.value.trim().toLowerCase());
const focusedID = ref("");

watch(() => props.root, () => {
  zoomID.value = "";
  focusedID.value = "";
});

watch(graphRoot, () => {
  focusedID.value = "";
});

function zoom(frame: FlameGraphFrame): void {
  if (hasChildren(frame)) zoomID.value = frame.node.id;
}

function handleFrameKey(event: KeyboardEvent, frame: FlameGraphFrame): void {
  if (event.key === "Enter" || event.key === " ") {
    event.preventDefault();
    zoom(frame);
    return;
  }
  const frames = zoomableFrames.value;
  if (frames.length < 2 || !["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown", "Home", "End"].includes(event.key)) return;
  event.preventDefault();
  const currentIndex = Math.max(0, frames.findIndex((candidate) => candidate.node.id === frame.node.id));
  let nextIndex = currentIndex;
  if (event.key === "Home") nextIndex = 0;
  else if (event.key === "End") nextIndex = frames.length - 1;
  else if (event.key === "ArrowRight" || event.key === "ArrowDown") nextIndex = (currentIndex + 1) % frames.length;
  else nextIndex = (currentIndex - 1 + frames.length) % frames.length;
  const nextID = frames[nextIndex].node.id;
  focusedID.value = nextID;
  const svg = (event.currentTarget as SVGElement | null)?.ownerSVGElement;
  void nextTick(() => svg?.querySelector<SVGElement>(`[data-frame-id="${nextID}"]`)?.focus());
}

function frameTabIndex(frame: FlameGraphFrame): number {
  const activeID = focusedID.value || zoomableFrames.value[0]?.node.id;
  return frame.node.id === activeID ? 0 : -1;
}

function isMatch(frame: FlameGraphFrame): boolean {
  return normalizedQuery.value !== "" && frame.node.name.toLowerCase().includes(normalizedQuery.value);
}

function frameColor(frame: FlameGraphFrame): string {
  let hash = 0;
  for (let i = 0; i < frame.node.name.length; i++) {
    hash = (Math.imul(hash, 31) + frame.node.name.charCodeAt(i)) | 0;
  }
  const hue = 8 + Math.abs(hash % 42);
  const lightness = 48 + Math.min(frame.depth, 5) * 3;
  return `hsl(${hue} 78% ${lightness}%)`;
}

function frameText(frame: FlameGraphFrame): string {
  const maxChars = Math.max(0, Math.floor(frame.width / 7));
  if (maxChars < 4) return "";
  if (frame.node.name.length <= maxChars) return frame.node.name;
  return `${frame.node.name.slice(0, maxChars - 1)}…`;
}

function formatValue(value: number): string {
  if (props.unit === "nanoseconds" || props.unit === "ns") {
    if (value >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(2)} s`;
    if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(2)} ms`;
    if (value >= 1_000) return `${(value / 1_000).toFixed(2)} µs`;
    return `${value} ns`;
  }
  if (props.unit === "bytes") {
    if (value >= 1024 * 1024) return `${(value / (1024 * 1024)).toFixed(2)} MB`;
    if (value >= 1024) return `${(value / 1024).toFixed(2)} KB`;
    return `${value} B`;
  }
  return `${value} ${props.unit || "samples"}`;
}

function percent(value: number): string {
  const total = graphRoot.value?.value ?? 0;
  return total > 0 ? `${(value / total * 100).toFixed(2)}%` : "0%";
}

function frameLabel(frame: FlameGraphFrame): string {
  return `${frame.node.name} · ${formatValue(frame.node.value)} · ${percent(frame.node.value)}`;
}
</script>

<template>
  <section class="flame-graph" data-test="flame-graph">
    <header class="flame-graph__toolbar">
      <span class="flame-graph__title">Flame graph</span>
      <input v-model="query" class="flame-graph__search" type="search" placeholder="Search frames" :aria-label="t('a11y.searchFlameGraphFrames')" />
      <button class="flame-graph__reset" type="button" :disabled="!zoomID" :title="t('a11y.resetFlameGraphZoom')" :aria-label="t('a11y.resetFlameGraphZoom')" @click="zoomID = ''">
        <el-icon :size="14"><RefreshLeft /></el-icon>
      </button>
    </header>
    <div v-if="layout && graphRoot" class="flame-graph__viewport">
      <svg class="flame-graph__svg" :viewBox="`0 0 1000 ${layout.height}`" :style="{ height: `${Math.max(72, layout.height)}px` }" role="group" :aria-label="t('a11y.profileFlameGraph')">
        <g
          v-for="frame in layout.frames"
          :key="frame.node.id"
          class="flame-graph__frame"
          :class="{ 'flame-graph__frame--interactive': hasChildren(frame), 'flame-graph__frame--match': isMatch(frame) }"
          :data-frame-id="frame.node.id"
          :role="hasChildren(frame) ? 'button' : undefined"
          :tabindex="hasChildren(frame) ? frameTabIndex(frame) : undefined"
          :aria-label="hasChildren(frame) ? frameLabel(frame) : undefined"
          @click="zoom(frame)"
          @keydown="handleFrameKey($event, frame)"
        >
          <title>{{ frameLabel(frame) }}</title>
          <rect :x="frame.x + 0.5" :y="frame.y + 0.5" :width="Math.max(0, frame.width - 1)" :height="frame.height - 1" rx="1" :fill="frameColor(frame)" />
          <text v-if="frameText(frame)" :x="frame.x + 4" :y="frame.y + 16">{{ frameText(frame) }}</text>
        </g>
      </svg>
    </div>
    <p v-else class="flame-graph__empty">No stack samples in this profile.</p>
  </section>
</template>

<style scoped>
.flame-graph { display: flex; min-height: 120px; flex-direction: column; border-top: 1px solid var(--color-border-subtle); }
.flame-graph__toolbar { display: flex; min-height: 34px; align-items: center; gap: 8px; padding: 4px 0; }
.flame-graph__title { flex: 1; font-size: 11px; font-weight: 600; text-transform: uppercase; }
.flame-graph__search { width: min(220px, 40%); min-width: 100px; padding: 3px 7px; border: 1px solid var(--color-border-default); border-radius: var(--radius-sm); color: var(--color-text-primary); background: var(--color-bg-surface); font-size: 11px; }
.flame-graph__reset { display: grid; width: 26px; height: 26px; place-items: center; border: 0; border-radius: var(--radius-sm); color: var(--color-text-secondary); background: transparent; cursor: pointer; }
.flame-graph__reset:hover:not(:disabled) { color: var(--color-text-primary); background: var(--chrome-hover-bg); }
.flame-graph__reset:disabled { opacity: 0.35; cursor: default; }
.flame-graph__viewport { width: 100%; max-height: 360px; overflow: auto; background: var(--color-bg-base); }
.flame-graph__svg { display: block; width: 100%; min-width: 680px; }
.flame-graph__frame { outline: none; }
.flame-graph__frame--interactive { cursor: pointer; }
.flame-graph__frame rect { stroke: rgba(0, 0, 0, 0.28); stroke-width: 0.6; }
.flame-graph__frame text { pointer-events: none; fill: #21150f; font-family: var(--font-mono); font-size: 10px; }
.flame-graph__frame:hover rect, .flame-graph__frame:focus-visible rect { stroke: var(--color-text-primary); stroke-width: 1.5; filter: brightness(1.08); }
.flame-graph__frame--match rect { stroke: #fff; stroke-width: 2; filter: saturate(1.25) brightness(1.1); }
.flame-graph__empty { padding: 18px 8px; color: var(--color-text-tertiary); font-size: 11px; }
</style>
