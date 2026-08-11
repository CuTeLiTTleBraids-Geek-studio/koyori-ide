<script setup lang="ts">
// Koyori IDE 组件 · Layout Split View。
// 喵，这是 Layout Split View，负责 Koyori IDE 的界面呈现喵~
// N-30: Recursively renders a split node in the layout tree.
// A split has an orientation (horizontal/vertical) and 2+ children,
// each of which is either a leaf or another split.
//
// N-53/Proposal P/N-54: Drag handles between adjacent children allow
// the user to resize splits by dragging (pointer events) or by keyboard
// (arrow keys, Home/End). Handles have full ARIA attributes per the
// WAI-ARIA Separator pattern.
//
// Vue SFCs can self-reference by their filename. Since this file is
// named LayoutSplitView.vue, the template can use <LayoutSplitView>
// recursively. This is Vue's built-in recursive component support.
import { ref, onUnmounted } from "vue";
import type { LayoutSplit } from "@/types";
import LayoutLeafView from "./LayoutLeafView.vue";
import { useI18n } from "@/lib/i18n";

const props = defineProps<{
  node: LayoutSplit;
  activeLeafId: string | null;
}>();

const emit = defineEmits<{
  activate: [leafId: string];
  // N-53: Emitted when a drag or keyboard resize ends so the parent
  // can persist the layout to the backend.
  resizeend: [];
}>();

const { t } = useI18n();

// Ref to the split container element. Used to measure its bounding rect
// during drag operations (converting pixel deltas to percentages).
const containerEl = ref<HTMLElement | null>(null);

// Minimum child size in percent. Prevents children from being dragged
// to zero width/height (which would make them unusable).
const MIN_CHILD_PCT = 5;
// Keyboard adjustment step in percent (N-54 a11y).
const KEYBOARD_STEP = 5;
// Drag handle thickness in pixels. Must match the CSS width/height.
const HANDLE_THICKNESS = 4;

function handleActivate(id: string) {
  emit("activate", id);
}

// N-53: Returns the flex-grow value for child i. Using flex-grow instead
// of flex-basis percentage ensures children always fill the container
// and maintain their relative proportions even when the window resizes
// (fixes the flexShrink: 0 overflow issue from the original code).
function childSizeGrow(index: number): number {
  const s = props.node.sizes?.[index];
  if (typeof s === "number" && s > 0) return s;
  return 100 / props.node.children.length;
}

// --- N-53: Pointer drag state ---
interface DragState {
  // handleIndex k is between child k and child k+1
  handleIndex: number;
  // Starting pointer position (clientX or clientY)
  startPointer: number;
  // Starting sizes of the two adjacent children: [sizes[k], sizes[k+1]]
  startSizes: [number, number];
  // Container size in pixels (width for horizontal, height for vertical)
  containerSize: number;
  // Number of handles in this split — used to subtract handle thickness
  // from the available space for accurate percentage calculation
  numHandles: number;
}

let dragState: DragState | null = null;

function onHandlePointerDown(e: PointerEvent, handleIndex: number): void {
  if (e.button !== 0) return;
  e.preventDefault();
  e.stopPropagation();

  const container = containerEl.value;
  if (!container) return;

  const rect = container.getBoundingClientRect();
  const isHorizontal = props.node.orientation === "horizontal";
  const containerSize = isHorizontal ? rect.width : rect.height;
  if (containerSize <= 0) return;

  const k = handleIndex;
  const startSizes: [number, number] = [
    childSizeGrow(k),
    childSizeGrow(k + 1),
  ];

  dragState = {
    handleIndex,
    startPointer: isHorizontal ? e.clientX : e.clientY,
    startSizes,
    containerSize,
    numHandles: props.node.children.length - 1,
  };

  window.addEventListener("pointermove", onPointerMove);
  window.addEventListener("pointerup", onPointerUp);
}

function onPointerMove(e: PointerEvent): void {
  if (!dragState) return;
  e.preventDefault();

  const isHorizontal = props.node.orientation === "horizontal";
  const currentPos = isHorizontal ? e.clientX : e.clientY;
  const deltaPx = currentPos - dragState.startPointer;
  // Available space for children = container minus all handle thicknesses
  const availablePx = dragState.containerSize - dragState.numHandles * HANDLE_THICKNESS;
  if (availablePx <= 0) return;
  const deltaPct = (deltaPx / availablePx) * 100;

  // Moving pointer right/down increases child[k] and decreases child[k+1]
  applySizeDelta(dragState.handleIndex, deltaPct, dragState.startSizes);
}

function onPointerUp(): void {
  if (!dragState) return;
  dragState = null;
  window.removeEventListener("pointermove", onPointerMove);
  window.removeEventListener("pointerup", onPointerUp);
  emit("resizeend");
}

// --- N-54: Keyboard support ---
function onHandleKeyDown(e: KeyboardEvent, handleIndex: number): void {
  const isHorizontal = props.node.orientation === "horizontal";
  const k = handleIndex;

  // Current sizes of the two adjacent children
  const size0 = childSizeGrow(k);
  const size1 = childSizeGrow(k + 1);

  switch (e.key) {
    case "ArrowLeft":
      if (!isHorizontal) return;
      e.preventDefault();
      adjustSizes(k, -KEYBOARD_STEP, size0, size1);
      break;
    case "ArrowRight":
      if (!isHorizontal) return;
      e.preventDefault();
      adjustSizes(k, KEYBOARD_STEP, size0, size1);
      break;
    case "ArrowUp":
      if (isHorizontal) return;
      e.preventDefault();
      adjustSizes(k, -KEYBOARD_STEP, size0, size1);
      break;
    case "ArrowDown":
      if (isHorizontal) return;
      e.preventDefault();
      adjustSizes(k, KEYBOARD_STEP, size0, size1);
      break;
    case "Home":
      // Minimize child[k] (set to minimum)
      e.preventDefault();
      adjustSizes(k, -(size0 - MIN_CHILD_PCT), size0, size1);
      break;
    case "End":
      // Maximize child[k] (set child[k+1] to minimum)
      e.preventDefault();
      adjustSizes(k, size1 - MIN_CHILD_PCT, size0, size1);
      break;
  }
}

// Apply a percentage delta to two adjacent children, starting from
// the given startSizes. Clamps both to MIN_CHILD_PCT.
function applySizeDelta(
  handleIndex: number,
  deltaPct: number,
  startSizes: [number, number],
): void {
  const k = handleIndex;
  const total = startSizes[0] + startSizes[1];
  let newSize0 = startSizes[0] + deltaPct;
  let newSize1 = startSizes[1] - deltaPct;

  // Clamp to minimum, redistributing the remainder to the other child
  if (newSize0 < MIN_CHILD_PCT) {
    newSize0 = MIN_CHILD_PCT;
    newSize1 = total - MIN_CHILD_PCT;
  }
  if (newSize1 < MIN_CHILD_PCT) {
    newSize1 = MIN_CHILD_PCT;
    newSize0 = total - MIN_CHILD_PCT;
  }

  // Ensure sizes array exists and has the right length
  // N-25 layout engine: the layout tree is a shared reactive structure
  // owned by the layout store. Child nodes mutate their own `sizes` in
  // place — this is intentional, not a prop-mutation bug. The parent
  // persists the tree on `resizeend`.
  /* eslint-disable vue/no-mutating-props */
  if (!props.node.sizes) {
    props.node.sizes = props.node.children.map((_, i) => childSizeGrow(i));
  }
  props.node.sizes[k] = newSize0;
  props.node.sizes[k + 1] = newSize1;
  /* eslint-enable vue/no-mutating-props */
}

// Keyboard-specific adjustment: reads current sizes, applies delta,
// and emits resizeend for persistence.
function adjustSizes(
  handleIndex: number,
  deltaPct: number,
  size0: number,
  size1: number,
): void {
  applySizeDelta(handleIndex, deltaPct, [size0, size1]);
  emit("resizeend");
}

// Clean up listeners if the component unmounts during a drag
onUnmounted(() => {
  if (dragState) {
    dragState = null;
    window.removeEventListener("pointermove", onPointerMove);
    window.removeEventListener("pointerup", onPointerUp);
  }
});
</script>

<template>
  <div
    ref="containerEl"
    :class="['layout-split', `layout-split--${node.orientation}`]"
  >
    <template v-for="(child, i) in node.children" :key="child.id">
      <div
        class="layout-split__child"
        :style="{ flexGrow: childSizeGrow(i), flexBasis: '0', flexShrink: 0 }"
      >
        <LayoutLeafView
          v-if="child.type === 'leaf'"
          :leaf="child"
          :active="child.id === activeLeafId"
          @activate="handleActivate"
        />
        <LayoutSplitView
          v-else
          :node="child"
          :active-leaf-id="activeLeafId"
          @activate="handleActivate"
          @resizeend="emit('resizeend')"
        />
      </div>
      <!-- N-53/Proposal P: Drag handle between adjacent children.
           Handle i is between child i and child i+1. -->
      <div
        v-if="i < node.children.length - 1"
        class="layout-split__handle"
        :class="`layout-split__handle--${node.orientation}`"
        role="separator"
        tabindex="0"
        :aria-orientation="node.orientation === 'horizontal' ? 'vertical' : 'horizontal'"
        :aria-valuenow="Math.round(childSizeGrow(i))"
        :aria-valuemin="MIN_CHILD_PCT"
        :aria-valuemax="100 - MIN_CHILD_PCT"
        :aria-label="t('layout.dragHandle')"
        @pointerdown="onHandlePointerDown($event, i)"
        @keydown="onHandleKeyDown($event, i)"
      />
    </template>
  </div>
</template>

<style scoped>
.layout-split {
  display: flex;
  flex: 1;
  min-width: 0;
  min-height: 0;
  overflow: hidden;
}

.layout-split--horizontal {
  flex-direction: row;
}

.layout-split--vertical {
  flex-direction: column;
}

.layout-split__child {
  min-width: 0;
  min-height: 0;
  overflow: hidden;
}

/* N-53/Proposal P: Drag handle between adjacent children */
.layout-split__handle {
  flex-shrink: 0;
  flex-grow: 0;
  background-color: var(--color-border-subtle);
  transition: background-color var(--transition-fast);
  z-index: 5;
}

.layout-split__handle:hover,
.layout-split__handle:focus-visible {
  background-color: var(--color-primary, #4285f4);
  outline: none;
}

.layout-split__handle--horizontal {
  width: 4px;
  cursor: col-resize;
  height: 100%;
}

.layout-split__handle--vertical {
  height: 4px;
  cursor: row-resize;
  width: 100%;
}
</style>
