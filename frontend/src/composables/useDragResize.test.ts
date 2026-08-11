import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { useDragResize } from "./useDragResize";

// jsdom doesn't always have PointerEvent; use MouseEvent with the right type
// string — the composable only reads button/clientX/clientY and calls
// preventDefault/stopPropagation, all of which MouseEvent provides.
function makePointerEvent(
  type: string,
  opts: { clientX?: number; clientY?: number; button?: number } = {},
): PointerEvent {
  return new MouseEvent(type, {
    bubbles: true,
    cancelable: true,
    clientX: opts.clientX ?? 0,
    clientY: opts.clientY ?? 0,
    button: opts.button ?? 0,
  }) as unknown as PointerEvent;
}

describe("useDragResize (N-20)", () => {
  let size: number;
  let commits: number[];

  beforeEach(() => {
    size = 300;
    commits = [];
  });

  afterEach(() => {
    // Clean up any active drags from tests that didn't dispatch pointerup.
    // Without this, window listeners accumulate across tests (onUnmounted
    // doesn't fire in test context).
    window.dispatchEvent(makePointerEvent("pointerup"));
    vi.restoreAllMocks();
  });

  function makeHorizontal() {
    return useDragResize({
      direction: "horizontal",
      sign: "positive-increases",
      min: 100,
      max: 500,
      getStartSize: () => size,
      onResize: (s) => { size = s; },
      onCommit: (s) => { commits.push(s); },
    });
  }

  describe("onPointerDown", () => {
    it("starts dragging on primary button", () => {
      const drag = makeHorizontal();
      const e = makePointerEvent("pointerdown", { clientX: 100, button: 0 });
      drag.onPointerDown(e);
      expect(drag.dragging.value).toBe(true);
      expect(e.defaultPrevented).toBe(true);
    });

    it("ignores non-primary button", () => {
      const drag = makeHorizontal();
      const e = makePointerEvent("pointerdown", { clientX: 100, button: 1 });
      drag.onPointerDown(e);
      expect(drag.dragging.value).toBe(false);
    });
  });

  describe("horizontal drag (positive-increases)", () => {
    it("increases size when dragging right", () => {
      const drag = makeHorizontal();
      drag.onPointerDown(makePointerEvent("pointerdown", { clientX: 100 }));
      window.dispatchEvent(makePointerEvent("pointermove", { clientX: 150 }));
      expect(size).toBe(350);
    });

    it("decreases size when dragging left", () => {
      const drag = makeHorizontal();
      drag.onPointerDown(makePointerEvent("pointerdown", { clientX: 100 }));
      window.dispatchEvent(makePointerEvent("pointermove", { clientX: 50 }));
      expect(size).toBe(250);
    });

    it("clamps to max", () => {
      size = 450;
      const drag = makeHorizontal();
      drag.onPointerDown(makePointerEvent("pointerdown", { clientX: 100 }));
      window.dispatchEvent(makePointerEvent("pointermove", { clientX: 300 }));
      expect(size).toBe(500);
    });

    it("clamps to min", () => {
      size = 150;
      const drag = makeHorizontal();
      drag.onPointerDown(makePointerEvent("pointerdown", { clientX: 100 }));
      window.dispatchEvent(makePointerEvent("pointermove", { clientX: 0 }));
      expect(size).toBe(100);
    });
  });

  describe("horizontal drag (positive-decreases)", () => {
    function makeDecreasing() {
      return useDragResize({
        direction: "horizontal",
        sign: "positive-decreases",
        min: 100,
        max: 500,
        getStartSize: () => size,
        onResize: (s) => { size = s; },
        onCommit: (s) => { commits.push(s); },
      });
    }

    it("decreases size when dragging right (AI panel right edge)", () => {
      const drag = makeDecreasing();
      drag.onPointerDown(makePointerEvent("pointerdown", { clientX: 100 }));
      window.dispatchEvent(makePointerEvent("pointermove", { clientX: 150 }));
      expect(size).toBe(250);
    });

    it("increases size when dragging left", () => {
      const drag = makeDecreasing();
      drag.onPointerDown(makePointerEvent("pointerdown", { clientX: 100 }));
      window.dispatchEvent(makePointerEvent("pointermove", { clientX: 50 }));
      expect(size).toBe(350);
    });
  });

  describe("vertical drag", () => {
    function makeVertical() {
      return useDragResize({
        direction: "vertical",
        sign: "positive-decreases",
        min: 80,
        max: 600,
        getStartSize: () => size,
        onResize: (s) => { size = s; },
        onCommit: (s) => { commits.push(s); },
      });
    }

    it("decreases size when dragging down (terminal top edge)", () => {
      size = 220;
      const drag = makeVertical();
      drag.onPointerDown(makePointerEvent("pointerdown", { clientY: 100 }));
      window.dispatchEvent(makePointerEvent("pointermove", { clientY: 150 }));
      expect(size).toBe(170);
    });

    it("increases size when dragging up", () => {
      size = 220;
      const drag = makeVertical();
      drag.onPointerDown(makePointerEvent("pointerdown", { clientY: 100 }));
      window.dispatchEvent(makePointerEvent("pointermove", { clientY: 50 }));
      expect(size).toBe(270);
    });
  });

  describe("onCommit", () => {
    it("calls onCommit with final size on pointerup", () => {
      const drag = makeHorizontal();
      drag.onPointerDown(makePointerEvent("pointerdown", { clientX: 100 }));
      window.dispatchEvent(makePointerEvent("pointermove", { clientX: 150 }));
      window.dispatchEvent(makePointerEvent("pointerup"));
      expect(commits).toEqual([350]);
      expect(drag.dragging.value).toBe(false);
    });

    it("does not call onCommit if drag never started", () => {
      makeHorizontal();
      // pointerup without a prior pointerdown should be a no-op.
      window.dispatchEvent(makePointerEvent("pointerup"));
      expect(commits).toEqual([]);
    });
  });

  describe("listener cleanup", () => {
    it("removes window listeners after pointerup", () => {
      const drag = makeHorizontal();
      drag.onPointerDown(makePointerEvent("pointerdown", { clientX: 100 }));
      window.dispatchEvent(makePointerEvent("pointerup"));
      // After pointerup, pointermove should not fire onResize.
      const before = size;
      window.dispatchEvent(makePointerEvent("pointermove", { clientX: 200 }));
      expect(size).toBe(before);
    });

    it("stops dragging if pointerup fires without pointermove", () => {
      const drag = makeHorizontal();
      drag.onPointerDown(makePointerEvent("pointerdown", { clientX: 100 }));
      window.dispatchEvent(makePointerEvent("pointerup"));
      expect(drag.dragging.value).toBe(false);
    });
  });

  describe("onCommit optional", () => {
    it("works without onCommit", () => {
      const drag = useDragResize({
        direction: "horizontal",
        sign: "positive-increases",
        min: 100,
        max: 500,
        getStartSize: () => size,
        onResize: (s) => { size = s; },
      });
      drag.onPointerDown(makePointerEvent("pointerdown", { clientX: 100 }));
      window.dispatchEvent(makePointerEvent("pointermove", { clientX: 150 }));
      window.dispatchEvent(makePointerEvent("pointerup"));
      expect(size).toBe(350);
    });
  });

  // N-54: Keyboard support (WAI-ARIA separator pattern)
  describe("onKeyDown (N-54 a11y)", () => {
    function makeKey(key: string, opts: { shift?: boolean } = {}): KeyboardEvent {
      let prevented = false;
      return {
        key,
        shiftKey: opts.shift ?? false,
        preventDefault: () => { prevented = true; },
        get defaultPrevented() { return prevented; },
      } as unknown as KeyboardEvent;
    }

    it("ArrowRight increases size by step", () => {
      size = 300;
      const drag = makeHorizontal(); // min=100, max=500, step = 20
      drag.onKeyDown(makeKey("ArrowRight"));
      expect(size).toBe(320);
    });

    it("ArrowLeft decreases size by step", () => {
      size = 300;
      const drag = makeHorizontal();
      drag.onKeyDown(makeKey("ArrowLeft"));
      expect(size).toBe(280);
    });

    it("ArrowDown increases size for vertical handle", () => {
      size = 200;
      const drag = useDragResize({
        direction: "vertical",
        sign: "positive-decreases",
        min: 80,
        max: 600,
        getStartSize: () => size,
        onResize: (s) => { size = s; },
      });
      drag.onKeyDown(makeKey("ArrowDown"));
      expect(size).toBeGreaterThan(200);
    });

    it("ArrowUp decreases size for vertical handle", () => {
      size = 200;
      const drag = useDragResize({
        direction: "vertical",
        sign: "positive-decreases",
        min: 80,
        max: 600,
        getStartSize: () => size,
        onResize: (s) => { size = s; },
      });
      drag.onKeyDown(makeKey("ArrowUp"));
      expect(size).toBeLessThan(200);
    });

    it("Shift doubles the step", () => {
      size = 300;
      const drag = makeHorizontal(); // step = 20, shift = 40
      drag.onKeyDown(makeKey("ArrowRight", { shift: true }));
      expect(size).toBe(340);
    });

    it("Home sets size to min", () => {
      size = 300;
      const drag = makeHorizontal(); // min=100
      drag.onKeyDown(makeKey("Home"));
      expect(size).toBe(100);
    });

    it("End sets size to max", () => {
      size = 300;
      const drag = makeHorizontal(); // max=500
      drag.onKeyDown(makeKey("End"));
      expect(size).toBe(500);
    });

    it("clamps to max on ArrowRight", () => {
      size = 490;
      const drag = makeHorizontal(); // max=500, step=20
      drag.onKeyDown(makeKey("ArrowRight"));
      expect(size).toBe(500);
    });

    it("clamps to min on ArrowLeft", () => {
      size = 110;
      const drag = makeHorizontal(); // min=100, step=20
      drag.onKeyDown(makeKey("ArrowLeft"));
      expect(size).toBe(100);
    });

    it("calls onCommit after keyboard resize", () => {
      size = 300;
      const drag = makeHorizontal();
      drag.onKeyDown(makeKey("ArrowRight"));
      expect(commits).toEqual([320]);
    });

    it("does not resize for unhandled keys", () => {
      size = 300;
      const drag = makeHorizontal();
      const e = makeKey("Enter");
      drag.onKeyDown(e);
      expect(size).toBe(300);
      expect(e.defaultPrevented).toBe(false);
    });

    it("prevents default for handled keys", () => {
      size = 300;
      const drag = makeHorizontal();
      const e = makeKey("ArrowRight");
      drag.onKeyDown(e);
      expect(e.defaultPrevented).toBe(true);
    });

    it("does not call onCommit when size unchanged", () => {
      size = 500;
      const drag = makeHorizontal(); // max=500
      commits.length = 0;
      drag.onKeyDown(makeKey("ArrowRight")); // already at max
      expect(commits).toEqual([]);
    });

    it("exposes ariaMin, ariaMax, getCurrentValue", () => {
      size = 300;
      const drag = makeHorizontal(); // min=100, max=500
      expect(drag.ariaMin).toBe(100);
      expect(drag.ariaMax).toBe(500);
      expect(drag.getCurrentValue()).toBe(300);
    });
  });
});
