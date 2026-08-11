// Koyori IDE 模块 · Flame Graph。
// 喵，这是 Koyori IDE 的 Flame Graph 模块（前端实现）~
import type { FlameGraphNode } from "@/types";

export interface FlameGraphFrame {
  node: FlameGraphNode;
  x: number;
  y: number;
  width: number;
  height: number;
  depth: number;
}

export interface FlameGraphLayout {
  frames: FlameGraphFrame[];
  height: number;
  maxDepth: number;
}

export function layoutFlameGraph(
  root: FlameGraphNode,
  totalWidth = 1000,
  rowHeight = 24,
): FlameGraphLayout {
  const frames: FlameGraphFrame[] = [];
  let maxDepth = 0;
  const pending: Array<{ node: FlameGraphNode; x: number; width: number; depth: number }> = [
    { node: root, x: 0, width: totalWidth, depth: 0 },
  ];

  while (pending.length > 0) {
    const current = pending.pop()!;
    const { node, x, width, depth } = current;
    frames.push({ node, x, y: 0, width, height: rowHeight, depth });
    maxDepth = Math.max(maxDepth, depth);
    if (node.value <= 0 || width <= 0) continue;

    const children = node.children ?? [];
    const childFrames: typeof pending = [];
    let childX = x;
    for (const child of children) {
      const childWidth = width * Math.max(0, child.value) / node.value;
      childFrames.push({ node: child, x: childX, width: childWidth, depth: depth + 1 });
      childX += childWidth;
    }
    for (let index = childFrames.length - 1; index >= 0; index--) {
      pending.push(childFrames[index]);
    }
  }

  for (const frame of frames) {
    frame.y = (maxDepth - frame.depth) * rowHeight;
  }
  return { frames, height: (maxDepth + 1) * rowHeight, maxDepth };
}

export function findFlameNode(root: FlameGraphNode, id: string): FlameGraphNode | null {
	const pending = [root];
	while (pending.length > 0) {
		const node = pending.pop()!;
		if (node.id === id) return node;
		for (const child of node.children ?? []) pending.push(child);
	}
	return null;
}
