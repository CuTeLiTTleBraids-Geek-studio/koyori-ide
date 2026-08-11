import { describe, it, expect, beforeEach, vi } from "vitest";
import { mount } from "@vue/test-utils";
import type { LayoutSplit } from "@/types";

// Mock i18n to avoid pulling in the full i18n setup.
vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({
    t: (key: string) => {
      const map: Record<string, string> = {
        "layout.dragHandle": "Resize split",
      };
      return map[key] ?? key;
    },
  }),
}));

// Mock LayoutLeafView to avoid the Monaco/editor import chain.
// We just need to verify it renders — no need for the real component.
vi.mock("@/components/layout/LayoutLeafView.vue", () => ({
  default: {
    name: "LayoutLeafView",
    template: '<div class="stub-leaf" :data-leaf-id="leaf.id" />',
    props: ["leaf", "active"],
    emits: ["activate"],
  },
}));

import LayoutSplitView from "./LayoutSplitView.vue";

// Helper: create a simple 2-child horizontal split with equal sizes.
function makeHorizontalSplit(sizes?: number[]): LayoutSplit {
  return {
    id: "split-1",
    type: "split",
    orientation: "horizontal",
    children: [
      { id: "leaf-a", type: "leaf", viewId: "editor" },
      { id: "leaf-b", type: "leaf", viewId: "editor" },
    ],
    sizes: sizes ?? [50, 50],
  };
}

// Helper: create a 3-child horizontal split.
function makeThreeChildSplit(sizes?: number[]): LayoutSplit {
  return {
    id: "split-3",
    type: "split",
    orientation: "horizontal",
    children: [
      { id: "leaf-a", type: "leaf", viewId: "editor" },
      { id: "leaf-b", type: "leaf", viewId: "editor" },
      { id: "leaf-c", type: "leaf", viewId: "editor" },
    ],
    sizes: sizes ?? [33.33, 33.33, 33.34],
  };
}

// Helper: create a vertical 2-child split.
function makeVerticalSplit(sizes?: number[]): LayoutSplit {
  return {
    id: "split-v",
    type: "split",
    orientation: "vertical",
    children: [
      { id: "leaf-a", type: "leaf", viewId: "editor" },
      { id: "leaf-b", type: "leaf", viewId: "editor" },
    ],
    sizes: sizes ?? [50, 50],
  };
}

// Helper: simulate a pointer drag sequence on a handle.
// getBoundingClientRect is mocked to return a container of given size.
function mockContainerSize(width: number, height: number) {
  // jsdom doesn't implement getBoundingClientRect — we must mock it.
  // We attach the mock to Element.prototype so any element returns it.
  const original = Element.prototype.getBoundingClientRect;
  Element.prototype.getBoundingClientRect = function () {
    return {
      width,
      height,
      top: 0,
      left: 0,
      right: width,
      bottom: height,
      x: 0,
      y: 0,
      toJSON: () => {},
    } as DOMRect;
  };
  return () => {
    Element.prototype.getBoundingClientRect = original;
  };
}

describe("LayoutSplitView — N-53/Proposal P/N-54", () => {
  beforeEach(() => {
    // Reset window event listeners between tests
    vi.restoreAllMocks();
  });

  // --- Render tests ---

  it("renders drag handles between children for a 2-child split", () => {
    const node = makeHorizontalSplit();
    const wrapper = mount(LayoutSplitView, {
      props: { node, activeLeafId: null },
    });
    const handles = wrapper.findAll(".layout-split__handle");
    // 2 children → 1 handle
    expect(handles).toHaveLength(1);
  });

  it("renders N-1 drag handles for N children", () => {
    const node = makeThreeChildSplit();
    const wrapper = mount(LayoutSplitView, {
      props: { node, activeLeafId: null },
    });
    const handles = wrapper.findAll(".layout-split__handle");
    // 3 children → 2 handles
    expect(handles).toHaveLength(2);
  });

  it("does not render a handle after the last child", () => {
    const node = makeHorizontalSplit();
    const wrapper = mount(LayoutSplitView, {
      props: { node, activeLeafId: null },
    });
    // The handle should be between children, not after the last one.
    // Check that the last child div is NOT followed by a handle.
    const children = wrapper.findAll(".layout-split__child");
    const handles = wrapper.findAll(".layout-split__handle");
    expect(children).toHaveLength(2);
    expect(handles).toHaveLength(1);
  });

  // --- ARIA tests (N-54) ---

  it("handle has role=separator", () => {
    const node = makeHorizontalSplit();
    const wrapper = mount(LayoutSplitView, {
      props: { node, activeLeafId: null },
    });
    const handle = wrapper.find(".layout-split__handle");
    expect(handle.attributes("role")).toBe("separator");
  });

  it("handle has tabindex=0", () => {
    const node = makeHorizontalSplit();
    const wrapper = mount(LayoutSplitView, {
      props: { node, activeLeafId: null },
    });
    const handle = wrapper.find(".layout-split__handle");
    expect(handle.attributes("tabindex")).toBe("0");
  });

  it("horizontal split has aria-orientation=vertical on handles", () => {
    const node = makeHorizontalSplit();
    const wrapper = mount(LayoutSplitView, {
      props: { node, activeLeafId: null },
    });
    const handle = wrapper.find(".layout-split__handle");
    // A horizontal split (row) has vertical separators between children
    expect(handle.attributes("aria-orientation")).toBe("vertical");
  });

  it("vertical split has aria-orientation=horizontal on handles", () => {
    const node = makeVerticalSplit();
    const wrapper = mount(LayoutSplitView, {
      props: { node, activeLeafId: null },
    });
    const handle = wrapper.find(".layout-split__handle");
    // A vertical split (column) has horizontal separators between children
    expect(handle.attributes("aria-orientation")).toBe("horizontal");
  });

  it("aria-valuenow reflects the first child's size", () => {
    const node = makeHorizontalSplit([30, 70]);
    const wrapper = mount(LayoutSplitView, {
      props: { node, activeLeafId: null },
    });
    const handle = wrapper.find(".layout-split__handle");
    expect(handle.attributes("aria-valuenow")).toBe("30");
  });

  it("aria-valuemin and aria-valuemax are set", () => {
    const node = makeHorizontalSplit();
    const wrapper = mount(LayoutSplitView, {
      props: { node, activeLeafId: null },
    });
    const handle = wrapper.find(".layout-split__handle");
    expect(handle.attributes("aria-valuemin")).toBe("5");
    expect(handle.attributes("aria-valuemax")).toBe("95");
  });

  it("handle has aria-label", () => {
    const node = makeHorizontalSplit();
    const wrapper = mount(LayoutSplitView, {
      props: { node, activeLeafId: null },
    });
    const handle = wrapper.find(".layout-split__handle");
    expect(handle.attributes("aria-label")).toBe("Resize split");
  });

  // --- Pointer drag tests (N-53) ---

  it("pointer drag updates sizes on pointermove", async () => {
    const restoreRect = mockContainerSize(1000, 600);
    const node = makeHorizontalSplit([50, 50]);
    const wrapper = mount(LayoutSplitView, {
      props: { node, activeLeafId: null },
    });

    const handle = wrapper.find(".layout-split__handle");
    // Start drag at x=500 (the middle of a 1000px container)
    handle.element.dispatchEvent(
      new PointerEvent("pointerdown", { clientX: 500, clientY: 0, button: 0, bubbles: true }),
    );

    // Move pointer 100px right — child 0 should grow by ~10%
    window.dispatchEvent(
      new PointerEvent("pointermove", { clientX: 600, clientY: 0 }),
    );

    // Check sizes were updated
    expect(node.sizes![0]).toBeCloseTo(60, 0);
    expect(node.sizes![1]).toBeCloseTo(40, 0);

    // End drag
    window.dispatchEvent(new PointerEvent("pointerup", {}));

    restoreRect();
  });

  it("pointer drag emits resizeend on pointerup", async () => {
    const restoreRect = mockContainerSize(1000, 600);
    const node = makeHorizontalSplit();
    const wrapper = mount(LayoutSplitView, {
      props: { node, activeLeafId: null },
    });

    const handle = wrapper.find(".layout-split__handle");
    handle.element.dispatchEvent(
      new PointerEvent("pointerdown", { clientX: 500, clientY: 0, button: 0, bubbles: true }),
    );
    window.dispatchEvent(
      new PointerEvent("pointermove", { clientX: 550, clientY: 0 }),
    );
    window.dispatchEvent(new PointerEvent("pointerup", {}));

    const resizeendEvents = wrapper.emitted("resizeend");
    expect(resizeendEvents).toBeTruthy();
    expect(resizeendEvents!.length).toBe(1);

    restoreRect();
  });

  it("pointer drag enforces minimum child size", async () => {
    const restoreRect = mockContainerSize(1000, 600);
    const node = makeHorizontalSplit([50, 50]);
    const wrapper = mount(LayoutSplitView, {
      props: { node, activeLeafId: null },
    });

    const handle = wrapper.find(".layout-split__handle");
    // Start at x=500
    handle.element.dispatchEvent(
      new PointerEvent("pointerdown", { clientX: 500, clientY: 0, button: 0, bubbles: true }),
    );
    // Drag far left — child 0 should be clamped to 5%
    window.dispatchEvent(
      new PointerEvent("pointermove", { clientX: 0, clientY: 0 }),
    );

    expect(node.sizes![0]).toBe(5);
    expect(node.sizes![1]).toBe(95);

    window.dispatchEvent(new PointerEvent("pointerup", {}));
    restoreRect();
  });

  it("pointer drag in vertical orientation uses clientY", async () => {
    const restoreRect = mockContainerSize(800, 600);
    const node = makeVerticalSplit([50, 50]);
    const wrapper = mount(LayoutSplitView, {
      props: { node, activeLeafId: null },
    });

    const handle = wrapper.find(".layout-split__handle");
    // Start at y=300 (middle of 600px container)
    handle.element.dispatchEvent(
      new PointerEvent("pointerdown", { clientX: 0, clientY: 300, button: 0, bubbles: true }),
    );
    // Move 60px down — child 0 grows by ~10%
    window.dispatchEvent(
      new PointerEvent("pointermove", { clientX: 0, clientY: 360 }),
    );

    expect(node.sizes![0]).toBeCloseTo(60, 0);
    expect(node.sizes![1]).toBeCloseTo(40, 0);

    window.dispatchEvent(new PointerEvent("pointerup", {}));
    restoreRect();
  });

  it("ignores non-primary button pointerdown", async () => {
    const node = makeHorizontalSplit();
    const wrapper = mount(LayoutSplitView, {
      props: { node, activeLeafId: null },
    });

    const handle = wrapper.find(".layout-split__handle");
    // button=2 (right click) should be ignored
    handle.element.dispatchEvent(
      new PointerEvent("pointerdown", { clientX: 500, clientY: 0, button: 2, bubbles: true }),
    );

    // No pointermove listener should have been added
    window.dispatchEvent(
      new PointerEvent("pointermove", { clientX: 600, clientY: 0 }),
    );

    // Sizes should not have changed
    expect(node.sizes![0]).toBe(50);
    expect(node.sizes![1]).toBe(50);
  });

  // --- Keyboard tests (N-54) ---

  it("ArrowRight increases first child size by 5%", async () => {
    const node = makeHorizontalSplit([50, 50]);
    const wrapper = mount(LayoutSplitView, {
      props: { node, activeLeafId: null },
    });

    const handle = wrapper.find(".layout-split__handle");
    await handle.trigger("keydown", { key: "ArrowRight" });

    expect(node.sizes![0]).toBe(55);
    expect(node.sizes![1]).toBe(45);
  });

  it("ArrowLeft decreases first child size by 5%", async () => {
    const node = makeHorizontalSplit([50, 50]);
    const wrapper = mount(LayoutSplitView, {
      props: { node, activeLeafId: null },
    });

    const handle = wrapper.find(".layout-split__handle");
    await handle.trigger("keydown", { key: "ArrowLeft" });

    expect(node.sizes![0]).toBe(45);
    expect(node.sizes![1]).toBe(55);
  });

  it("ArrowDown increases first child size in vertical split", async () => {
    const node = makeVerticalSplit([50, 50]);
    const wrapper = mount(LayoutSplitView, {
      props: { node, activeLeafId: null },
    });

    const handle = wrapper.find(".layout-split__handle");
    await handle.trigger("keydown", { key: "ArrowDown" });

    expect(node.sizes![0]).toBe(55);
    expect(node.sizes![1]).toBe(45);
  });

  it("ArrowUp decreases first child size in vertical split", async () => {
    const node = makeVerticalSplit([50, 50]);
    const wrapper = mount(LayoutSplitView, {
      props: { node, activeLeafId: null },
    });

    const handle = wrapper.find(".layout-split__handle");
    await handle.trigger("keydown", { key: "ArrowUp" });

    expect(node.sizes![0]).toBe(45);
    expect(node.sizes![1]).toBe(55);
  });

  it("ArrowLeft is ignored in vertical split", async () => {
    const node = makeVerticalSplit([50, 50]);
    const wrapper = mount(LayoutSplitView, {
      props: { node, activeLeafId: null },
    });

    const handle = wrapper.find(".layout-split__handle");
    await handle.trigger("keydown", { key: "ArrowLeft" });

    // Should not change — ArrowLeft is only for horizontal splits
    expect(node.sizes![0]).toBe(50);
    expect(node.sizes![1]).toBe(50);
  });

  it("Home key minimizes first child to 5%", async () => {
    const node = makeHorizontalSplit([50, 50]);
    const wrapper = mount(LayoutSplitView, {
      props: { node, activeLeafId: null },
    });

    const handle = wrapper.find(".layout-split__handle");
    await handle.trigger("keydown", { key: "Home" });

    expect(node.sizes![0]).toBe(5);
    expect(node.sizes![1]).toBe(95);
  });

  it("End key maximizes first child (sets second to 5%)", async () => {
    const node = makeHorizontalSplit([50, 50]);
    const wrapper = mount(LayoutSplitView, {
      props: { node, activeLeafId: null },
    });

    const handle = wrapper.find(".layout-split__handle");
    await handle.trigger("keydown", { key: "End" });

    expect(node.sizes![0]).toBe(95);
    expect(node.sizes![1]).toBe(5);
  });

  it("keyboard resize emits resizeend", async () => {
    const node = makeHorizontalSplit();
    const wrapper = mount(LayoutSplitView, {
      props: { node, activeLeafId: null },
    });

    const handle = wrapper.find(".layout-split__handle");
    await handle.trigger("keydown", { key: "ArrowRight" });

    const events = wrapper.emitted("resizeend");
    expect(events).toBeTruthy();
    expect(events!.length).toBe(1);
  });

  it("keyboard enforces minimum size", async () => {
    const node = makeHorizontalSplit([10, 90]);
    const wrapper = mount(LayoutSplitView, {
      props: { node, activeLeafId: null },
    });

    const handle = wrapper.find(".layout-split__handle");
    // Pressing ArrowLeft twice should clamp to 5%
    await handle.trigger("keydown", { key: "ArrowLeft" });
    // Now 5/95 — second ArrowLeft shouldn't go below 5%
    await handle.trigger("keydown", { key: "ArrowLeft" });

    expect(node.sizes![0]).toBe(5);
    expect(node.sizes![1]).toBe(95);
  });

  // --- Multiple handles test ---

  it("second handle in a 3-child split resizes children 1 and 2", async () => {
    const node = makeThreeChildSplit([33.33, 33.33, 33.34]);
    const wrapper = mount(LayoutSplitView, {
      props: { node, activeLeafId: null },
    });

    const handles = wrapper.findAll(".layout-split__handle");
    expect(handles).toHaveLength(2);

    // Use the second handle (between child 1 and child 2)
    await handles[1].trigger("keydown", { key: "ArrowRight" });

    // child 1 grows by 5, child 2 shrinks by 5
    expect(node.sizes![1]).toBeCloseTo(38.33, 1);
    expect(node.sizes![2]).toBeCloseTo(28.34, 1);
    // child 0 is unaffected
    expect(node.sizes![0]).toBeCloseTo(33.33, 1);
  });

  // --- Default sizes (no sizes array) ---

  it("works without a sizes array (defaults to equal)", async () => {
    const node: LayoutSplit = {
      id: "split-1",
      type: "split",
      orientation: "horizontal",
      children: [
        { id: "leaf-a", type: "leaf", viewId: "editor" },
        { id: "leaf-b", type: "leaf", viewId: "editor" },
      ],
      // No sizes array
    };
    const wrapper = mount(LayoutSplitView, {
      props: { node, activeLeafId: null },
    });

    const handle = wrapper.find(".layout-split__handle");
    // aria-valuenow should reflect the default equal split (50)
    expect(handle.attributes("aria-valuenow")).toBe("50");

    // Keyboard resize should initialize the sizes array and update it
    await handle.trigger("keydown", { key: "ArrowRight" });
    expect(node.sizes).toBeDefined();
    expect(node.sizes![0]).toBe(55);
    expect(node.sizes![1]).toBe(45);
  });

  // --- Recursive nested splits ---

  it("nested LayoutSplitView propagates resizeend event", async () => {
    const node: LayoutSplit = {
      id: "outer",
      type: "split",
      orientation: "horizontal",
      children: [
        { id: "leaf-a", type: "leaf", viewId: "editor" },
        {
          id: "inner",
          type: "split",
          orientation: "vertical",
          children: [
            { id: "leaf-b", type: "leaf", viewId: "editor" },
            { id: "leaf-c", type: "leaf", viewId: "editor" },
          ],
          sizes: [50, 50],
        },
      ],
      sizes: [50, 50],
    };

    const wrapper = mount(LayoutSplitView, {
      props: { node, activeLeafId: null },
    });

    // Find the inner split's handle — it's the second handle in the DOM
    // (the first is the outer split's handle)
    const handles = wrapper.findAll(".layout-split__handle");
    // Outer: 1 handle, inner: 1 handle → total 2
    expect(handles).toHaveLength(2);

    // Trigger keyboard resize on the inner handle
    await handles[1].trigger("keydown", { key: "ArrowDown" });

    // resizeend should bubble up through the recursive component chain
    const events = wrapper.emitted("resizeend");
    expect(events).toBeTruthy();
    expect(events!.length).toBe(1);

    // Inner split's sizes should be updated
    const innerSplit = node.children[1] as LayoutSplit;
    expect(innerSplit.sizes![0]).toBe(55);
    expect(innerSplit.sizes![1]).toBe(45);
  });
});
