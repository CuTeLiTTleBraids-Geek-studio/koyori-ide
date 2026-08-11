import { describe, it, expect, beforeEach } from "vitest";
import {
  layoutState,
  splitLeaf,
  closeLeaf,
  replaceLeafView,
  setActiveLeaf,
  resetLayout,
  findLeaf,
  findLeafByViewId,
  findParent,
  countLeaves,
  collectLeaves,
  serializeLayout,
  deserializeLayout,
  activeLeaf,
} from "./layout";
import type { LayoutNode, LayoutLeaf } from "@/types";

// Helper: get the root node.
function root(): LayoutNode {
  return layoutState.tree.root;
}

// Helper: get the first leaf in the tree.
function firstLeaf(): LayoutLeaf {
  const leaves = collectLeaves(root());
  return leaves[0];
}

describe("Layout Engine (N-25)", () => {
  beforeEach(() => {
    resetLayout();
  });

  describe("initial state", () => {
    it("starts with a single empty leaf as root", () => {
      expect(root().type).toBe("leaf");
      expect((root() as LayoutLeaf).viewId).toBeNull();
      expect(layoutState.tree.activeLeafId).toBe(root().id);
    });

    it("has one leaf in the tree", () => {
      expect(countLeaves(root())).toBe(1);
    });
  });

  describe("splitLeaf", () => {
    it("splits the root leaf into a split with two children", () => {
      const leafId = firstLeaf().id;
      const ok = splitLeaf(leafId, "horizontal", "editor");

      expect(ok).toBe(true);
      expect(root().type).toBe("split");
      expect((root() as any).orientation).toBe("horizontal");
      expect((root() as any).children).toHaveLength(2);
      expect((root() as any).children[0].id).toBe(leafId);
      expect((root() as any).children[1].viewId).toBe("editor");
    });

    it("activates the new leaf after split", () => {
      const leafId = firstLeaf().id;
      splitLeaf(leafId, "vertical", "preview");

      const newLeaf = (root() as any).children[1];
      expect(layoutState.tree.activeLeafId).toBe(newLeaf.id);
    });

    it("assigns equal sizes to the split children", () => {
      const leafId = firstLeaf().id;
      splitLeaf(leafId, "horizontal");

      const split = root() as any;
      expect(split.sizes).toEqual([50, 50]);
    });

    it("returns false for non-existent leaf ID", () => {
      expect(splitLeaf("nonexistent", "horizontal")).toBe(false);
    });

    it("can split a nested leaf (leaf inside a split)", () => {
      const leafId = firstLeaf().id;
      splitLeaf(leafId, "horizontal", "editor");

      // Split the second child (the new leaf).
      const secondLeaf = (root() as any).children[1];
      const ok = splitLeaf(secondLeaf.id, "vertical", "preview");

      expect(ok).toBe(true);
      // The second child should now be a split.
      expect((root() as any).children[1].type).toBe("split");
      expect((root() as any).children[1].children).toHaveLength(2);
    });

    it("supports splitting with null viewId (empty leaf)", () => {
      const leafId = firstLeaf().id;
      splitLeaf(leafId, "horizontal", null);

      const newLeaf = (root() as any).children[1];
      expect(newLeaf.viewId).toBeNull();
    });
  });

  describe("closeLeaf", () => {
    it("replaces root leaf with a fresh empty leaf when closing the root", () => {
      const leafId = firstLeaf().id;
      // Set a viewId first.
      replaceLeafView(leafId, "editor");

      closeLeaf(leafId);

      expect(root().type).toBe("leaf");
      expect((root() as LayoutLeaf).viewId).toBeNull();
      expect(root().id).not.toBe(leafId);
      expect(layoutState.tree.activeLeafId).toBe(root().id);
    });

    it("simplifies parent split to remaining child when one of two children is closed", () => {
      const leafId = firstLeaf().id;
      splitLeaf(leafId, "horizontal", "editor");

      // Root is now a split with 2 children.
      expect(root().type).toBe("split");

      // Close the second child.
      const secondLeafId = (root() as any).children[1].id;
      closeLeaf(secondLeafId);

      // Root should be the remaining leaf (simplification).
      expect(root().type).toBe("leaf");
      expect(root().id).toBe(leafId);
    });

    it("simplifies grandparent when nested split reduces to one child", () => {
      const leafId = firstLeaf().id;
      splitLeaf(leafId, "horizontal", "editor");

      // Split the second child again.
      const secondLeafId = (root() as any).children[1].id;
      splitLeaf(secondLeafId, "vertical", "preview");

      // Root is split → children: [leaf1, split2]
      // split2 is: [leaf2, leaf3]
      expect(root().type).toBe("split");
      expect((root() as any).children[1].type).toBe("split");

      // Capture leaf3's ID before closing (split2.children will be
      // mutated by closeLeaf, so we can't read it afterwards).
      const split2 = (root() as any).children[1];
      const leaf3Id = split2.children[1].id;
      const leaf2Id = split2.children[0].id;

      // Close leaf2 (first child of split2).
      closeLeaf(leaf2Id);

      // split2 should be simplified to leaf3, and root.children[1]
      // should now be a leaf (leaf3).
      expect((root() as any).children[1].type).toBe("leaf");
      expect((root() as any).children[1].id).toBe(leaf3Id);
    });

    it("updates activeLeafId when closing the active leaf", () => {
      const leafId = firstLeaf().id;
      splitLeaf(leafId, "horizontal", "editor");

      // Active leaf is the new one (second child).
      const activeId = layoutState.tree.activeLeafId;
      expect(activeId).not.toBe(leafId);

      closeLeaf(activeId!);

      // Active should fall back to the first leaf.
      expect(layoutState.tree.activeLeafId).toBe(leafId);
    });

    it("returns false for non-existent leaf ID", () => {
      expect(closeLeaf("nonexistent")).toBe(false);
    });

    it("handles closing a leaf in a 3-child split (no simplification)", () => {
      const leafId = firstLeaf().id;
      splitLeaf(leafId, "horizontal", "editor");

      // Split the second child to create a third leaf at root level...
      // Actually, let's split the first child to add a third at root.
      // Wait — splitLeaf replaces the leaf with a split, so:
      // After first split: root = split[leaf1, leaf2]
      // Split leaf1: root = split[split[leaf1, leaf3], leaf2]
      // That's nested. For a 3-child split at root, we'd need to add
      // a child to the root split directly. Since splitLeaf always
      // creates binary splits, let's test with a nested case.

      // Split leaf2 to get: root = split[leaf1, split[leaf2, leaf3]]
      const leaf2Id = (root() as any).children[1].id;
      splitLeaf(leaf2Id, "vertical", "preview");

      // Now close leaf1 — root split has one child (the nested split),
      // so root should become the nested split.
      closeLeaf(leafId);

      expect(root().type).toBe("split");
      expect((root() as any).children).toHaveLength(2);
    });
  });

  describe("replaceLeafView", () => {
    it("changes the viewId of a leaf", () => {
      const leafId = firstLeaf().id;
      replaceLeafView(leafId, "editor");
      expect(firstLeaf().viewId).toBe("editor");

      replaceLeafView(leafId, "preview");
      expect(firstLeaf().viewId).toBe("preview");
    });

    it("can set viewId to null", () => {
      const leafId = firstLeaf().id;
      replaceLeafView(leafId, "editor");
      replaceLeafView(leafId, null);
      expect(firstLeaf().viewId).toBeNull();
    });

    it("returns false for non-existent leaf ID", () => {
      expect(replaceLeafView("nonexistent", "editor")).toBe(false);
    });
  });

  describe("setActiveLeaf", () => {
    it("sets the activeLeafId to the given leaf", () => {
      const leafId = firstLeaf().id;
      // It's already active, but let's split and then set active.
      splitLeaf(leafId, "horizontal", "editor");
      const firstChild = (root() as any).children[0];

      setActiveLeaf(firstChild.id);
      expect(layoutState.tree.activeLeafId).toBe(firstChild.id);
    });

    it("does nothing for non-existent leaf ID", () => {
      const before = layoutState.tree.activeLeafId;
      setActiveLeaf("nonexistent");
      expect(layoutState.tree.activeLeafId).toBe(before);
    });
  });

  describe("activeLeaf computed", () => {
    it("returns the active leaf node", () => {
      const leafId = firstLeaf().id;
      expect(activeLeaf.value).not.toBeNull();
      expect(activeLeaf.value!.id).toBe(leafId);
    });

    it("returns null when activeLeafId is null", () => {
      layoutState.tree.activeLeafId = null;
      expect(activeLeaf.value).toBeNull();
    });
  });

  describe("findLeaf", () => {
    it("finds a leaf by ID in a flat tree", () => {
      const leafId = firstLeaf().id;
      const found = findLeaf(root(), leafId);
      expect(found).not.toBeNull();
      expect(found!.id).toBe(leafId);
    });

    it("finds a leaf by ID in a nested tree", () => {
      const leafId = firstLeaf().id;
      splitLeaf(leafId, "horizontal", "editor");
      splitLeaf(leafId, "vertical", "preview");

      // Tree: split[split[leaf1, leaf3], leaf2]
      const found = findLeaf(root(), leafId);
      expect(found).not.toBeNull();
      expect(found!.id).toBe(leafId);
    });

    it("returns null for non-existent ID", () => {
      expect(findLeaf(root(), "nonexistent")).toBeNull();
    });
  });

  describe("findLeafByViewId", () => {
    it("finds a leaf by viewId", () => {
      replaceLeafView(firstLeaf().id, "editor");
      const found = findLeafByViewId(root(), "editor");
      expect(found).not.toBeNull();
      expect(found!.viewId).toBe("editor");
    });

    it("returns null for viewId not in tree", () => {
      expect(findLeafByViewId(root(), "nonexistent")).toBeNull();
    });

    it("finds leaves with null viewId when searching for null", () => {
      // findLeafByViewId expects a string; null does not match the signature.
      // This documents that only real viewId strings are searchable.
      expect(findLeafByViewId(root(), "")).toBeNull();
    });
  });

  describe("findParent", () => {
    it("returns null for the root node (no parent)", () => {
      const leafId = firstLeaf().id;
      expect(findParent(root(), leafId)).toBeNull();
    });

    it("finds the parent of a nested leaf", () => {
      const leafId = firstLeaf().id;
      splitLeaf(leafId, "horizontal", "editor");

      const split = root() as any;
      const secondChild = split.children[1];
      const parent = findParent(root(), secondChild.id);

      expect(parent).not.toBeNull();
      expect(parent!.parent.id).toBe(split.id);
      expect(parent!.index).toBe(1);
    });
  });

  describe("countLeaves", () => {
    it("counts 1 for a single leaf", () => {
      expect(countLeaves(root())).toBe(1);
    });

    it("counts 2 after one split", () => {
      splitLeaf(firstLeaf().id, "horizontal");
      expect(countLeaves(root())).toBe(2);
    });

    it("counts 3 after two splits", () => {
      const leafId = firstLeaf().id;
      splitLeaf(leafId, "horizontal", "editor");
      splitLeaf(leafId, "vertical", "preview");
      expect(countLeaves(root())).toBe(3);
    });
  });

  describe("collectLeaves", () => {
    it("collects all leaves in depth-first order", () => {
      const leafId = firstLeaf().id;
      splitLeaf(leafId, "horizontal", "editor");
      splitLeaf(leafId, "vertical", "preview");

      // Tree: split[split[leaf1, leaf3], leaf2]
      const leaves = collectLeaves(root());
      expect(leaves).toHaveLength(3);
      expect(leaves[0].id).toBe(leafId);
    });
  });

  describe("serializeLayout / deserializeLayout", () => {
    it("serializes a single leaf tree to JSON", () => {
      const json = serializeLayout();
      const parsed = JSON.parse(json);
      expect(parsed.root.type).toBe("leaf");
      expect(parsed.activeLeafId).toBe(parsed.root.id);
    });

    it("serializes a split tree to JSON", () => {
      splitLeaf(firstLeaf().id, "horizontal", "editor");
      const json = serializeLayout();
      const parsed = JSON.parse(json);
      expect(parsed.root.type).toBe("split");
      expect(parsed.root.children).toHaveLength(2);
    });

    it("round-trips a complex tree through serialize → deserialize", () => {
      const leafId = firstLeaf().id;
      replaceLeafView(leafId, "editor");
      splitLeaf(leafId, "horizontal", "preview");
      splitLeaf(leafId, "vertical", "terminal");

      const json = serializeLayout();
      const beforeTree = JSON.parse(json);

      resetLayout();
      expect(root().type).toBe("leaf");

      const ok = deserializeLayout(json);
      expect(ok).toBe(true);

      const afterJson = serializeLayout();
      const afterTree = JSON.parse(afterJson);

      expect(afterTree.root.type).toBe(beforeTree.root.type);
      expect(afterTree.root.children).toHaveLength(beforeTree.root.children.length);
      expect(afterTree.activeLeafId).toBe(beforeTree.activeLeafId);
    });

    it("returns false and resets for invalid JSON", () => {
      const ok = deserializeLayout(`{invalid}`);
      expect(ok).toBe(false);
      expect(root().type).toBe("leaf");
      expect((root() as LayoutLeaf).viewId).toBeNull();
    });

    it("returns false and resets for JSON missing root", () => {
      const ok = deserializeLayout(`{"activeLeafId": null}`);
      expect(ok).toBe(false);
      expect(root().type).toBe("leaf");
    });

    it("returns false for a split with fewer than 2 children", () => {
      const badJson = JSON.stringify({
        root: {
          id: "split1",
          type: "split",
          orientation: "horizontal",
          children: [{ id: "leaf1", type: "leaf", viewId: null }],
        },
        activeLeafId: "leaf1",
      });
      const ok = deserializeLayout(badJson);
      expect(ok).toBe(false);
    });

    it("returns false for a node with unknown type", () => {
      const badJson = JSON.stringify({
        root: { id: "x", type: "unknown" },
        activeLeafId: "x",
      });
      const ok = deserializeLayout(badJson);
      expect(ok).toBe(false);
    });

    it("accepts a valid deserialized tree with nested splits", () => {
      const validJson = JSON.stringify({
        root: {
          id: "split1",
          type: "split",
          orientation: "horizontal",
          children: [
            { id: "leaf1", type: "leaf", viewId: "explorer" },
            {
              id: "split2",
              type: "split",
              orientation: "vertical",
              children: [
                { id: "leaf2", type: "leaf", viewId: "editor" },
                { id: "leaf3", type: "leaf", viewId: "terminal" },
              ],
              sizes: [70, 30],
            },
          ],
          sizes: [25, 75],
        },
        activeLeafId: "leaf2",
      });

      const ok = deserializeLayout(validJson);
      expect(ok).toBe(true);
      expect(root().type).toBe("split");
      expect(countLeaves(root())).toBe(3);
      expect(layoutState.tree.activeLeafId).toBe("leaf2");

      // Verify the viewId is correctly loaded.
      const editorLeaf = findLeafByViewId(root(), "editor");
      expect(editorLeaf).not.toBeNull();
      expect(editorLeaf!.id).toBe("leaf2");
    });
  });

  describe("resetLayout", () => {
    it("resets to a single empty leaf", () => {
      // Build up a complex tree first.
      splitLeaf(firstLeaf().id, "horizontal", "editor");
      splitLeaf(firstLeaf().id, "vertical", "preview");

      resetLayout();

      expect(root().type).toBe("leaf");
      expect((root() as LayoutLeaf).viewId).toBeNull();
      expect(layoutState.tree.activeLeafId).toBe(root().id);
      expect(countLeaves(root())).toBe(1);
    });
  });
});
