// Koyori IDE 模块 · Use Drag Resize。
// 喵，这是 Koyori IDE 的 Use Drag Resize 模块（前端实现）~
import { ref, onUnmounted } from "vue";

/**
 * Direction the drag handle controls (N-20).
 * - "horizontal": dragging changes width (e.g. sidebar right edge, AI panel left edge)
 * - "vertical": dragging changes height (e.g. terminal top edge)
 */
export type DragDirection = "horizontal" | "vertical";

/**
 * Sign convention for the delta:
 * - "positive-increases": moving the pointer right/down increases the size
 *   (e.g. sidebar right edge — dragging right makes it wider)
 * - "positive-decreases": moving the pointer right/down decreases the size
 *   (e.g. AI panel left edge — dragging right makes it narrower)
 */
export type DragSign = "positive-increases" | "positive-decreases";

interface UseDragResizeOptions {
  direction: DragDirection;
  sign: DragSign;
  /** Minimum size in pixels. */
  min: number;
  /** Maximum size in pixels. Use Infinity for unbounded. */
  max: number;
  /**
   * Returns the current size at call time. Called once on pointerdown (to
   * capture the start size) and again on pointerup (to read the final size
   * for commit). The caller should read from the same reactive source that
   * `onResize` writes to.
   */
  getStartSize: () => number;
  /** Called with the new size (clamped to [min, max]) on every pointer move. */
  onResize: (size: number) => void;
  /** Called after the drag ends with the final size. Useful for persistence. */
  onCommit?: (size: number) => void;
}

/**
 * useDragResize provides pointer-event handlers for a drag handle element.
 * Attach `onPointerDown` to the handle element. While dragging, the composable
 * tracks pointer movement and calls `onResize` with the updated size.
 *
 * The composable manages its own window-level pointermove/pointerup listeners
 * and cleans them up on unmount or when the drag ends.
 *
 * N-54: `onKeyDown` provides keyboard-operable resizing (WAI-ARIA separator
 * pattern). ArrowRight/ArrowDown increase the size by a step; ArrowLeft/ArrowUp
 * decrease it. Home sets the minimum; End sets the maximum. Shift doubles the
 * step. Each keypress commits the final size via `onCommit`.
 *
 * N-20: used for sidebar width, AI chat width, and terminal height drag handles.
 */
export function useDragResize(opts: UseDragResizeOptions) {
  const dragging = ref(false);
  let startPos = 0;
  let startSize = 0;

  // N-54: Keyboard step is 5% of the range, at least 1px.
  const keyboardStep = Math.max(1, Math.round((opts.max - opts.min) * 0.05));

  function onPointerDown(e: PointerEvent): void {
    // Only respond to primary button (left click).
    if (e.button !== 0) return;
    e.preventDefault();
    e.stopPropagation();
    dragging.value = true;
    startPos = opts.direction === "horizontal" ? e.clientX : e.clientY;
    startSize = opts.getStartSize();
    window.addEventListener("pointermove", onPointerMove);
    window.addEventListener("pointerup", onPointerUp);
  }

  function onPointerMove(e: PointerEvent): void {
    if (!dragging.value) return;
    e.preventDefault();
    const currentPos = opts.direction === "horizontal" ? e.clientX : e.clientY;
    const rawDelta = currentPos - startPos;
    const signedDelta =
      opts.sign === "positive-increases" ? rawDelta : -rawDelta;
    const next = clamp(startSize + signedDelta, opts.min, opts.max);
    opts.onResize(next);
  }

  function onPointerUp(): void {
    if (!dragging.value) return;
    dragging.value = false;
    window.removeEventListener("pointermove", onPointerMove);
    window.removeEventListener("pointerup", onPointerUp);
    const finalSize = opts.getStartSize();
    opts.onCommit?.(finalSize);
  }

  // N-54: Keyboard handler for WAI-ARIA separator pattern.
  // Right/Down = increase, Left/Up = decrease, Home = min, End = max.
  // Shift doubles the step size. Each press commits via onCommit.
  function onKeyDown(e: KeyboardEvent): void {
    const shift = e.shiftKey ? 2 : 1;
    const step = keyboardStep * shift;
    const current = opts.getStartSize();
    let next: number | null = null;
    switch (e.key) {
      case "ArrowRight":
      case "ArrowDown":
        next = clamp(current + step, opts.min, opts.max);
        break;
      case "ArrowLeft":
      case "ArrowUp":
        next = clamp(current - step, opts.min, opts.max);
        break;
      case "Home":
        next = opts.min;
        break;
      case "End":
        next = opts.max === Infinity ? current : opts.max;
        break;
      default:
        return; // ignore unhandled keys
    }
    e.preventDefault();
    if (next === current) return;
    opts.onResize(next);
    opts.onCommit?.(next);
  }

  onUnmounted(() => {
    if (dragging.value) {
      dragging.value = false;
      window.removeEventListener("pointermove", onPointerMove);
      window.removeEventListener("pointerup", onPointerUp);
    }
  });

  return {
    dragging,
    onPointerDown,
    onKeyDown,
    // N-54: expose min/max/current for ARIA attributes.
    ariaMin: opts.min,
    ariaMax: opts.max === Infinity ? undefined : opts.max,
    getCurrentValue: opts.getStartSize,
  };
}

function clamp(value: number, min: number, max: number): number {
  if (value < min) return min;
  if (value > max) return max;
  return value;
}
