import { describe, expect, it } from "vitest";
import { findFlameNode, layoutFlameGraph } from "./flameGraph";
import type { FlameGraphNode } from "@/types";

const root = {
  id: "0",
  name: "all",
  value: 100,
  children: [
    {
      id: "0.0",
      name: "root",
      value: 100,
      children: [
        { id: "0.0.0", name: "left", value: 70, children: [] },
        { id: "0.0.1", name: "right", value: 30, children: [] },
      ],
    },
  ],
};

describe("flame graph layout", () => {
  it("lays out children proportionally without overlap", () => {
    const layout = layoutFlameGraph(root, 1000, 24);
    const left = layout.frames.find(({ node }) => node.name === "left");
    const right = layout.frames.find(({ node }) => node.name === "right");

    expect(left).toMatchObject({ x: 0, width: 700, depth: 2 });
    expect(right).toMatchObject({ x: 700, width: 300, depth: 2 });
    expect(layout.height).toBe(72);
  });

  it("finds zoom roots by stable frame id", () => {
    expect(findFlameNode(root, "0.0.1")?.name).toBe("right");
    expect(findFlameNode(root, "missing")).toBeNull();
  });

  it("places roots below callees in Brendan Gregg flame graph order", () => {
    const layout = layoutFlameGraph(root);
    const rootFrame = layout.frames.find((frame) => frame.node.id === "0")!;
    const childFrame = layout.frames.find((frame) => frame.node.id === "0.0")!;
    const leafFrame = layout.frames.find((frame) => frame.node.id === "0.0.0")!;
    expect(rootFrame.y).toBeGreaterThan(childFrame.y);
    expect(childFrame.y).toBeGreaterThan(leafFrame.y);
  });

  it("handles a graph at the backend node limit without recursive overflow", () => {
    const deepRoot: FlameGraphNode = { id: "0", name: "0", value: 1, children: [] };
    let current = deepRoot;
    for (let index = 1; index < 10_000; index++) {
      const child: FlameGraphNode = { id: String(index), name: String(index), value: 1, children: [] };
      current.children = [child];
      current = child;
    }
    expect(layoutFlameGraph(deepRoot).frames).toHaveLength(10_000);
    expect(findFlameNode(deepRoot, "9999")?.name).toBe("9999");
  });
});
