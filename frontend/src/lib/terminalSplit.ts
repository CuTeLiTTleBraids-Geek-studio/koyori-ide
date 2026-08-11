// Koyori IDE 模块 · Terminal Split。
// 喵，这是 Koyori IDE 的 Terminal Split 模块（前端实现）~
export type SplitDirection = "horizontal" | "vertical";

export interface SplitNode {
  type: "split";
  id: string;
  direction: SplitDirection;
  children: [LayoutNode, LayoutNode];
  ratio: number;
}

export interface LeafNode {
  type: "leaf";
  id: string;
  sessionId: string;
}

export type LayoutNode = SplitNode | LeafNode;

export const DEFAULT_SPLIT_RATIO = 0.5;

let generatedNodeSequence = 0;

function createNodeId(kind: "leaf" | "split"): string {
  generatedNodeSequence += 1;
  const randomUUID = globalThis.crypto?.randomUUID?.();
  return randomUUID
    ? `${kind}:${randomUUID}`
    : `${kind}:${Date.now().toString(36)}:${generatedNodeSequence.toString(36)}`;
}

function clampRatio(ratio: number, fallback: number): number {
  return Number.isFinite(ratio) ? Math.min(1, Math.max(0, ratio)) : fallback;
}

export function createLeaf(sessionId: string): LeafNode {
  if (!sessionId.trim()) throw new Error("Terminal session id must not be empty");
  return { type: "leaf", id: createNodeId("leaf"), sessionId };
}

function createSplit(
  first: LayoutNode,
  direction: SplitDirection,
  newSessionId: string,
): SplitNode {
  return {
    type: "split",
    id: createNodeId("split"),
    direction,
    children: [first, createLeaf(newSessionId)],
    ratio: DEFAULT_SPLIT_RATIO,
  };
}

function isSplitDirection(value: string): value is SplitDirection {
  return value === "horizontal" || value === "vertical";
}

interface ReplaceResult {
  node: LayoutNode;
  changed: boolean;
}

function leafMatches(node: LeafNode, leafId: string): boolean {
  // Matching sessionId keeps the original three-argument API compatible.
  return node.id === leafId || node.sessionId === leafId;
}

function splitLeafById(
  node: LayoutNode,
  leafId: string,
  direction: SplitDirection,
  newSessionId: string,
): ReplaceResult {
  if (node.type === "leaf") {
    return leafMatches(node, leafId)
      ? { node: createSplit(node, direction, newSessionId), changed: true }
      : { node, changed: false };
  }

  const first = splitLeafById(
    node.children[0],
    leafId,
    direction,
    newSessionId,
  );
  if (first.changed) {
    return {
      node: { ...node, children: [first.node, node.children[1]] },
      changed: true,
    };
  }

  const second = splitLeafById(
    node.children[1],
    leafId,
    direction,
    newSessionId,
  );
  return second.changed
    ? {
        node: { ...node, children: [node.children[0], second.node] },
        changed: true,
      }
    : { node, changed: false };
}

/**
 * Replaces a leaf in a layout tree without mutating the input. The four-argument
 * form is the public layout API; the three-argument form remains available for
 * callers that already hold the leaf being split.
 */
export function splitLeaf(
  node: LayoutNode,
  leafId: string,
  direction: SplitDirection,
  newSessionId: string,
): LayoutNode;
export function splitLeaf(
  node: LeafNode,
  direction: SplitDirection,
  newSessionId: string,
): SplitNode;
export function splitLeaf(
  node: LayoutNode,
  leafIdOrDirection: string,
  directionOrSessionId: string,
  newSessionId?: string,
): LayoutNode {
  if (newSessionId === undefined) {
    if (node.type !== "leaf" || !isSplitDirection(leafIdOrDirection)) {
      return node;
    }
    return createSplit(node, leafIdOrDirection, directionOrSessionId);
  }

  if (!isSplitDirection(directionOrSessionId)) return node;
  return splitLeafById(
    node,
    leafIdOrDirection,
    directionOrSessionId,
    newSessionId,
  ).node;
}

interface RemoveResult {
  node: LayoutNode | null;
  removed: boolean;
}

function removeFirstLeaf(node: LayoutNode, leafId: string): RemoveResult {
  if (node.type === "leaf") {
    return leafMatches(node, leafId)
      ? { node: null, removed: true }
      : { node, removed: false };
  }

  const first = removeFirstLeaf(node.children[0], leafId);
  if (first.removed) {
    return first.node === null
      ? { node: node.children[1], removed: true }
      : {
          node: { ...node, children: [first.node, node.children[1]] },
          removed: true,
        };
  }

  const second = removeFirstLeaf(node.children[1], leafId);
  if (!second.removed) return { node, removed: false };
  return second.node === null
    ? { node: node.children[0], removed: true }
    : {
        node: { ...node, children: [node.children[0], second.node] },
        removed: true,
      };
}

/** Removes one leaf and promotes its sibling when a split becomes unary. */
export function removeLeaf(
  node: LayoutNode,
  leafId: string,
): LayoutNode | null {
  return removeFirstLeaf(node, leafId).node;
}

/** Returns a resized copy of a split node, clamped to the 0..1 range. */
export function resize(node: SplitNode, ratio: number): SplitNode {
  const nextRatio = clampRatio(ratio, node.ratio);
  return Object.is(nextRatio, node.ratio)
    ? node
    : { ...node, ratio: nextRatio };
}

function resizeSplitById(
  node: LayoutNode,
  splitId: string,
  ratio: number,
): ReplaceResult {
  if (node.type === "leaf") return { node, changed: false };
  if (node.id === splitId) {
    const resized = resize(node, ratio);
    return { node: resized, changed: resized !== node };
  }

  const first = resizeSplitById(node.children[0], splitId, ratio);
  if (first.changed) {
    return {
      node: { ...node, children: [first.node, node.children[1]] },
      changed: true,
    };
  }

  const second = resizeSplitById(node.children[1], splitId, ratio);
  return second.changed
    ? {
        node: { ...node, children: [node.children[0], second.node] },
        changed: true,
      }
    : { node, changed: false };
}

/** Resizes a split anywhere in a tree while preserving untouched branches. */
export function resizeSplit(
  node: LayoutNode,
  splitId: string,
  ratio: number,
): LayoutNode {
  return resizeSplitById(node, splitId, ratio).node;
}

export function findLeaf(
  node: LayoutNode,
  sessionId: string,
): LeafNode | null {
  if (node.type === "leaf") {
    return node.sessionId === sessionId ? node : null;
  }
  return (
    findLeaf(node.children[0], sessionId) ??
    findLeaf(node.children[1], sessionId)
  );
}

export function getAllLeaves(node: LayoutNode): LeafNode[] {
  if (node.type === "leaf") return [node];
  return [
    ...getAllLeaves(node.children[0]),
    ...getAllLeaves(node.children[1]),
  ];
}

export function serializeLayout(node: LayoutNode): string {
  return JSON.stringify(node);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

interface DeserializeState {
  ids: Set<string>;
  sessionIds: Set<string>;
  nodes: number;
}

const MAX_LAYOUT_DEPTH = 128;
const MAX_LAYOUT_NODES = 4096;

function readNodeId(
  value: unknown,
  kind: "leaf" | "split",
  state: DeserializeState,
): string | null {
  const id = typeof value === "string" && value.trim() ? value : createNodeId(kind);
  if (state.ids.has(id)) return null;
  state.ids.add(id);
  return id;
}

function deserializeNode(
  value: unknown,
  state: DeserializeState,
  depth: number,
): LayoutNode | null {
  state.nodes += 1;
  if (
    depth > MAX_LAYOUT_DEPTH ||
    state.nodes > MAX_LAYOUT_NODES ||
    !isRecord(value)
  ) {
    return null;
  }

  if (value.type === "leaf") {
    if (typeof value.sessionId !== "string" || !value.sessionId.trim()) {
      return null;
    }
    if (state.sessionIds.has(value.sessionId)) return null;
    state.sessionIds.add(value.sessionId);
    const id = readNodeId(value.id, "leaf", state);
    return id === null
      ? null
      : { type: "leaf", id, sessionId: value.sessionId };
  }

  if (value.type !== "split") return null;
  const direction = value.direction;
  if (typeof direction !== "string" || !isSplitDirection(direction)) return null;
  if (
    typeof value.ratio !== "number" ||
    !Number.isFinite(value.ratio) ||
    value.ratio < 0 ||
    value.ratio > 1 ||
    !Array.isArray(value.children) ||
    value.children.length !== 2
  ) {
    return null;
  }

  const id = readNodeId(value.id, "split", state);
  if (id === null) return null;
  const first = deserializeNode(value.children[0], state, depth + 1);
  const second = deserializeNode(value.children[1], state, depth + 1);
  if (first === null || second === null) return null;
  return {
    type: "split",
    id,
    direction,
    children: [first, second],
    ratio: value.ratio,
  };
}

export function deserializeLayout(json: string): LayoutNode | null {
  try {
    const parsed: unknown = JSON.parse(json);
    return deserializeNode(
      parsed,
      { ids: new Set<string>(), sessionIds: new Set<string>(), nodes: 0 },
      0,
    );
  } catch {
    return null;
  }
}

/** Stable key for recursive Vue rendering. */
export function layoutNodeKey(node: LayoutNode): string {
  return node.id;
}
