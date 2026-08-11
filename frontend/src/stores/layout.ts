// Layout engine store (N-25 / Plan 72).
//
// Manages a tree of split/leaf nodes that describes the IDE's main
// editor area structure. The tree can be serialized to JSON and
// persisted via the backend LayoutService (layout.json in the profile
// directory).
//
// Tree operations:
//   - splitLeaf: split a leaf into a split with two children
//   - closeLeaf: remove a leaf and simplify the tree
//   - replaceLeafView: change which view a leaf shows
//   - setActiveLeaf: track which leaf has focus
//
// The store is intentionally UI-agnostic — it manages the data model.
// Components read layoutState.tree and render splits/leaves accordingly.
// Koyori IDE 模块 · Layout。
// 喵，这是 Koyori IDE 的 Layout 模块（前端实现）~
import { reactive, computed } from "vue";
import type {
  LayoutNode,
  LayoutLeaf,
  LayoutSplit,
  LayoutTree,
  LayoutOrientation,
} from "@/types";

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

interface LayoutStoreState {
  tree: LayoutTree;
}

let idCounter = 0;

/** Generate a unique node ID. Uses a counter + timestamp for uniqueness. */
function genId(prefix: string): string {
  idCounter++;
  return `${prefix}-${Date.now().toString(36)}-${idCounter}`;
}

/** Create a new leaf node. */
function makeLeaf(viewId: string | null = null): LayoutLeaf {
  return { id: genId("leaf"), type: "leaf", viewId };
}

/** Create a new split node with the given children. */
function makeSplit(
  orientation: LayoutOrientation,
  children: LayoutNode[],
): LayoutSplit {
  return {
    id: genId("split"),
    type: "split",
    orientation,
    children,
    sizes: normalizeSizes(children.length),
  };
}

/** Generate equal sizes (percentages) for n children. */
function normalizeSizes(n: number): number[] {
  if (n <= 0) return [];
  const each = 100 / n;
  return Array(n).fill(each);
}

// Default layout: a single empty leaf.
function defaultTree(): LayoutTree {
  const leaf = makeLeaf(null);
  return { root: leaf, activeLeafId: leaf.id };
}

export const layoutState = reactive<LayoutStoreState>({
  tree: defaultTree(),
});

/** The active leaf node (computed). */
export const activeLeaf = computed<LayoutLeaf | null>(() => {
  if (!layoutState.tree.activeLeafId) return null;
  return findLeaf(layoutState.tree.root, layoutState.tree.activeLeafId);
});

// ---------------------------------------------------------------------------
// Tree traversal helpers
// ---------------------------------------------------------------------------

/**
 * Find a leaf node by ID. Returns null if not found or if the ID
 * refers to a split node.
 */
export function findLeaf(node: LayoutNode, leafId: string): LayoutLeaf | null {
  if (node.type === "leaf") {
    return node.id === leafId ? node : null;
  }
  for (const child of node.children) {
    const found = findLeaf(child, leafId);
    if (found) return found;
  }
  return null;
}

/**
 * Find a leaf node by its viewId. Returns the first match or null.
 */
export function findLeafByViewId(
  node: LayoutNode,
  viewId: string,
): LayoutLeaf | null {
  if (node.type === "leaf") {
    return node.viewId === viewId ? node : null;
  }
  for (const child of node.children) {
    const found = findLeafByViewId(child, viewId);
    if (found) return found;
  }
  return null;
}

/**
 * Find the parent split of a node, along with the index of the child.
 * Returns null if the node is the root (no parent).
 */
export function findParent(
  node: LayoutNode,
  childId: string,
): { parent: LayoutSplit; index: number } | null {
  if (node.type === "leaf") return null;
  for (let i = 0; i < node.children.length; i++) {
    if (node.children[i].id === childId) {
      return { parent: node, index: i };
    }
    const found = findParent(node.children[i], childId);
    if (found) return found;
  }
  return null;
}

/** Count all leaves in the tree. */
export function countLeaves(node: LayoutNode): number {
  if (node.type === "leaf") return 1;
  return node.children.reduce((sum, c) => sum + countLeaves(c), 0);
}

/** Collect all leaves in the tree (depth-first order). */
export function collectLeaves(node: LayoutNode): LayoutLeaf[] {
  if (node.type === "leaf") return [node];
  return node.children.flatMap(collectLeaves);
}

// ---------------------------------------------------------------------------
// Tree mutation operations
// ---------------------------------------------------------------------------

/**
 * Split a leaf into two leaves under a new split node. The existing
 * leaf's view stays in the first child; the second child gets the
 * newViewId (or null for an empty leaf).
 *
 * Returns true on success, false if the leaf wasn't found.
 */
export function splitLeaf(
  leafId: string,
  orientation: LayoutOrientation,
  newViewId: string | null = null,
): boolean {
  const tree = layoutState.tree;
  const leaf = findLeaf(tree.root, leafId);
  if (!leaf) return false;

  const newLeaf = makeLeaf(newViewId);
  const split = makeSplit(orientation, [leaf, newLeaf]);

  // If the leaf is the root, replace the root.
  if (tree.root.id === leafId) {
    tree.root = split;
  } else {
    const parentInfo = findParent(tree.root, leafId);
    if (!parentInfo) return false;
    parentInfo.parent.children[parentInfo.index] = split;
    parentInfo.parent.sizes = normalizeSizes(parentInfo.parent.children.length);
  }

  // Activate the new leaf.
  tree.activeLeafId = newLeaf.id;
  return true;
}

/**
 * Close (remove) a leaf from the tree. After removal:
 *   - If the leaf's parent split now has one child, the split is
 *     replaced by that child (simplification).
 *   - If the root itself is a leaf and is closed, a new empty leaf
 *     becomes the root.
 *
 * Returns true on success.
 */
export function closeLeaf(leafId: string): boolean {
  const tree = layoutState.tree;
  const leaf = findLeaf(tree.root, leafId);
  if (!leaf) return false;

  // Closing the root leaf → replace with a fresh empty leaf.
  if (tree.root.id === leafId) {
    const fresh = makeLeaf(null);
    tree.root = fresh;
    tree.activeLeafId = fresh.id;
    return true;
  }

  const parentInfo = findParent(tree.root, leafId);
  if (!parentInfo) return false;

  const { parent, index } = parentInfo;
  // Remove the leaf from the parent.
  parent.children.splice(index, 1);

  // If the parent now has only one child, replace the parent with
  // that child (unless the parent is the root).
  if (parent.children.length === 1) {
    const remaining = parent.children[0];
    if (tree.root.id === parent.id) {
      tree.root = remaining;
    } else {
      const grandparent = findParent(tree.root, parent.id);
      if (grandparent) {
        grandparent.parent.children[grandparent.index] = remaining;
        grandparent.parent.sizes = normalizeSizes(
          grandparent.parent.children.length,
        );
      }
    }
  } else {
    parent.sizes = normalizeSizes(parent.children.length);
  }

  // Update active leaf if we closed the active one.
  if (tree.activeLeafId === leafId) {
    const leaves = collectLeaves(tree.root);
    tree.activeLeafId = leaves.length > 0 ? leaves[0].id : null;
  }

  return true;
}

/**
 * Replace the viewId in a leaf. Used when the user picks a different
 * view for an existing leaf (e.g. switching from "editor" to "preview").
 */
export function replaceLeafView(leafId: string, viewId: string | null): boolean {
  const leaf = findLeaf(layoutState.tree.root, leafId);
  if (!leaf) return false;
  leaf.viewId = viewId;
  return true;
}

/** Set the active leaf by ID. No-op if the leaf doesn't exist. */
export function setActiveLeaf(leafId: string): void {
  const leaf = findLeaf(layoutState.tree.root, leafId);
  if (leaf) {
    layoutState.tree.activeLeafId = leafId;
  }
}

/** Reset the layout to a single empty leaf. */
export function resetLayout(): void {
  const fresh = makeLeaf(null);
  layoutState.tree = { root: fresh, activeLeafId: fresh.id };
}

// ---------------------------------------------------------------------------
// Serialization
// ---------------------------------------------------------------------------

/**
 * Serialize the layout tree to a JSON string. The format matches the
 * LayoutTree interface and can be persisted via LayoutService.SaveLayout.
 */
export function serializeLayout(): string {
  return JSON.stringify(layoutState.tree, null, 2);
}

/**
 * Deserialize a JSON string into the layout tree state. If the JSON is
 * invalid or missing required fields, the layout is reset to default.
 *
 * Returns true on success, false if the JSON was invalid (and the
 * layout was reset to default as a fallback).
 */
export function deserializeLayout(json: string): boolean {
  try {
    const parsed = JSON.parse(json) as LayoutTree;
    if (!parsed || !parsed.root || !parsed.root.type) {
      resetLayout();
      return false;
    }
    // Basic structural validation: root must be a valid node.
    if (!validateNode(parsed.root)) {
      resetLayout();
      return false;
    }
    layoutState.tree = parsed;
    return true;
  } catch {
    resetLayout();
    return false;
  }
}

/** Recursively validate a layout node's structure. */
function validateNode(node: LayoutNode): boolean {
  if (!node || !node.id || !node.type) return false;
  if (node.type === "leaf") return true;
  if (node.type === "split") {
    if (!node.orientation) return false;
    if (!Array.isArray(node.children) || node.children.length < 2) return false;
    return node.children.every(validateNode);
  }
  return false;
}

// ---------------------------------------------------------------------------
// Persistence (via backend LayoutService)
// ---------------------------------------------------------------------------

/**
 * Load the layout from the backend LayoutService. If no layout file
 * exists (first run) or loading fails, the default layout is kept.
 *
 * This function is safe to call at startup — errors are swallowed and
 * logged rather than thrown.
 */
export async function loadLayoutFromBackend(
  loadFn: () => Promise<string>,
  shouldApply: () => boolean = () => true,
): Promise<void> {
  try {
    const json = await loadFn();
    if (json && shouldApply()) {
      deserializeLayout(json);
    }
  } catch (e) {
    // Silently fall back to default layout — the user can still use
    // the IDE without a persisted layout.
    console.warn("Failed to load layout:", e);
  }
}

/**
 * Save the current layout to the backend LayoutService. Errors are
 * logged but not thrown — layout persistence is best-effort.
 */
export async function saveLayoutToBackend(
  saveFn: (json: string) => Promise<void>,
): Promise<void> {
  try {
    await saveFn(serializeLayout());
  } catch (e) {
    console.warn("Failed to save layout:", e);
  }
}

/**
 * Proposal H (prompt-4.md): Reset the layout to default both in-memory
 * and in the backend. Calls the backend ResetLayout (removes layout.json),
 * then resets the in-memory tree to a single empty leaf, and persists
 * the fresh default so subsequent loads are clean.
 *
 * Errors are logged but not thrown — even if the backend reset fails,
 * the in-memory reset still happens so the user gets immediate relief.
 */
export async function resetLayoutFromBackend(
  resetFn: () => Promise<void>,
  saveFn: (json: string) => Promise<void>,
): Promise<void> {
  try {
    await resetFn();
  } catch (e) {
    console.warn("Failed to reset layout on backend:", e);
  }
  resetLayout();
  await saveLayoutToBackend(saveFn);
}
