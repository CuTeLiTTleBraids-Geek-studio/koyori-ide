import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import {
  registerShortcut,
  unregisterShortcut,
  setCustomShortcut,
  resetCustomShortcut,
  resetAllCustomShortcuts,
  getCustomShortcuts,
  loadCustomShortcuts,
  hasCustomShortcut,
  findConflicts,
  listShortcuts,
  formatShortcutKey,
} from "./useKeyboard";
import type { ShortcutKeys } from "@/types";
import { effectScope } from "vue";

// Helper to create a keyboard event with the standard modifiers.
function makeKeyEvent(
  key: string,
  opts: { ctrl?: boolean; shift?: boolean; alt?: boolean; meta?: boolean } = {},
): KeyboardEvent {
  return new KeyboardEvent("keydown", {
    key,
    ctrlKey: !!opts.ctrl,
    shiftKey: !!opts.shift,
    altKey: !!opts.alt,
    metaKey: !!opts.meta,
    bubbles: true,
    cancelable: true,
  });
}

describe("useKeyboard customization (N-8)", () => {
  // Track registered shortcuts so we can unregister them after each test.
  const registered: { handler: () => void }[] = [];

  function register(label: string, keys: ShortcutKeys): { handler: ReturnType<typeof vi.fn> } {
    const handler = vi.fn();
    const shortcut = {
      key: keys.key,
      ctrl: keys.ctrl,
      shift: keys.shift,
      alt: keys.alt,
      label,
      handler,
    };
    registerShortcut(shortcut);
    registered.push(shortcut);
    return { handler };
  }

  beforeEach(() => {
    registered.length = 0;
  });

  afterEach(() => {
    // Unregister all shortcuts we added.
    for (const s of registered) {
      unregisterShortcut(s as never);
    }
    registered.length = 0;
    resetAllCustomShortcuts();
  });

  describe("formatShortcutKey", () => {
    it("formats a simple key", () => {
      expect(formatShortcutKey({ key: "p", ctrl: false, shift: false, alt: false })).toBe("P");
    });

    it("formats a key with modifiers", () => {
      expect(formatShortcutKey({ key: "p", ctrl: true, shift: true, alt: false })).toBe("Ctrl+Shift+P");
    });

    it("formats multi-character keys without uppercasing", () => {
      expect(formatShortcutKey({ key: "F5", ctrl: false, shift: false, alt: false })).toBe("F5");
    });

    it("includes alt", () => {
      expect(formatShortcutKey({ key: "k", ctrl: false, shift: false, alt: true })).toBe("Alt+K");
    });
  });

  describe("setCustomShortcut / getCustomShortcuts / hasCustomShortcut", () => {
    it("stores a custom override", () => {
      setCustomShortcut("Save File", { key: "s", ctrl: true, shift: false, alt: false });
      expect(hasCustomShortcut("Save File")).toBe(true);
      expect(getCustomShortcuts()["Save File"]).toEqual({
        key: "s",
        ctrl: true,
        shift: false,
        alt: false,
      });
    });

    it("returns a snapshot (mutations to the returned map don't affect internal state)", () => {
      setCustomShortcut("Save File", { key: "s", ctrl: true, shift: false, alt: false });
      const snap = getCustomShortcuts();
      snap["Save File"].key = "x";
      expect(getCustomShortcuts()["Save File"].key).toBe("s");
    });

    it("setCustomShortcut returns conflict list", () => {
      register("Save File", { key: "s", ctrl: true, shift: false, alt: false });
      register("Command Palette", { key: "p", ctrl: true, shift: true, alt: false });
      const { conflicts } = setCustomShortcut("Save File", {
        key: "p",
        ctrl: true,
        shift: true,
        alt: false,
      });
      expect(conflicts).toEqual(["Command Palette"]);
    });
  });

  describe("resetCustomShortcut", () => {
    it("removes a custom override", () => {
      setCustomShortcut("Save File", { key: "s", ctrl: true, shift: false, alt: false });
      expect(hasCustomShortcut("Save File")).toBe(true);
      resetCustomShortcut("Save File");
      expect(hasCustomShortcut("Save File")).toBe(false);
    });

    it("is a no-op when no override exists", () => {
      resetCustomShortcut("Nonexistent");
      expect(hasCustomShortcut("Nonexistent")).toBe(false);
    });
  });

  describe("resetAllCustomShortcuts", () => {
    it("clears all overrides", () => {
      setCustomShortcut("A", { key: "a", ctrl: false, shift: false, alt: false });
      setCustomShortcut("B", { key: "b", ctrl: false, shift: false, alt: false });
      resetAllCustomShortcuts();
      expect(getCustomShortcuts()).toEqual({});
    });
  });

  describe("loadCustomShortcuts", () => {
    it("replaces all overrides from a snapshot", () => {
      setCustomShortcut("Old", { key: "o", ctrl: false, shift: false, alt: false });
      loadCustomShortcuts({
        "Save File": { key: "s", ctrl: true, shift: false, alt: false },
        "Command Palette": { key: "p", ctrl: true, shift: true, alt: false },
      });
      expect(hasCustomShortcut("Old")).toBe(false);
      expect(hasCustomShortcut("Save File")).toBe(true);
      expect(hasCustomShortcut("Command Palette")).toBe(true);
    });

    it("clears when given undefined or null", () => {
      setCustomShortcut("A", { key: "a", ctrl: false, shift: false, alt: false });
      loadCustomShortcuts(undefined);
      expect(getCustomShortcuts()).toEqual({});
      setCustomShortcut("B", { key: "b", ctrl: false, shift: false, alt: false });
      loadCustomShortcuts(null);
      expect(getCustomShortcuts()).toEqual({});
    });

    it("defensively copies entries (mutating the input map after load doesn't affect state)", () => {
      const input: Record<string, ShortcutKeys> = {
        "Save File": { key: "s", ctrl: true, shift: false, alt: false },
      };
      loadCustomShortcuts(input);
      input["Save File"].key = "x";
      expect(getCustomShortcuts()["Save File"].key).toBe("s");
    });
  });

  describe("findConflicts", () => {
    it("detects conflicts with other shortcuts' defaults", () => {
      register("Save File", { key: "s", ctrl: true, shift: false, alt: false });
      register("Command Palette", { key: "p", ctrl: true, shift: true, alt: false });
      const conflicts = findConflicts("Save File", {
        key: "p",
        ctrl: true,
        shift: true,
        alt: false,
      });
      expect(conflicts).toEqual(["Command Palette"]);
    });

    it("detects conflicts with other shortcuts' custom overrides", () => {
      register("Save File", { key: "s", ctrl: true, shift: false, alt: false });
      register("Command Palette", { key: "p", ctrl: true, shift: true, alt: false });
      setCustomShortcut("Command Palette", { key: "k", ctrl: true, shift: false, alt: false });
      const conflicts = findConflicts("Save File", {
        key: "k",
        ctrl: true,
        shift: false,
        alt: false,
      });
      expect(conflicts).toEqual(["Command Palette"]);
    });

    it("excludes the label being checked", () => {
      register("Save File", { key: "s", ctrl: true, shift: false, alt: false });
      const conflicts = findConflicts("Save File", {
        key: "s",
        ctrl: true,
        shift: false,
        alt: false,
      });
      expect(conflicts).toEqual([]);
    });

    it("is case-insensitive on the key", () => {
      register("Save File", { key: "s", ctrl: true, shift: false, alt: false });
      const conflicts = findConflicts("Other", {
        key: "S",
        ctrl: true,
        shift: false,
        alt: false,
      });
      expect(conflicts).toEqual(["Save File"]);
    });

    it("returns empty when no shortcuts are registered", () => {
      const conflicts = findConflicts("Anything", {
        key: "x",
        ctrl: false,
        shift: false,
        alt: false,
      });
      expect(conflicts).toEqual([]);
    });
  });

  describe("listShortcuts", () => {
    it("returns defaults when no overrides are set", () => {
      register("Save File", { key: "s", ctrl: true, shift: false, alt: false });
      register("Command Palette", { key: "p", ctrl: true, shift: true, alt: false });
      const list = listShortcuts();
      expect(list).toHaveLength(2);
      expect(list[0]).toEqual({
        label: "Save File",
        keys: "Ctrl+S",
        isCustom: false,
        defaultKeys: "Ctrl+S",
      });
      expect(list[1]).toEqual({
        label: "Command Palette",
        keys: "Ctrl+Shift+P",
        isCustom: false,
        defaultKeys: "Ctrl+Shift+P",
      });
    });

    it("reflects custom overrides in keys and isCustom", () => {
      register("Save File", { key: "s", ctrl: true, shift: false, alt: false });
      setCustomShortcut("Save File", { key: "k", ctrl: true, shift: false, alt: false });
      const list = listShortcuts();
      expect(list[0]).toEqual({
        label: "Save File",
        keys: "Ctrl+K",
        isCustom: true,
        defaultKeys: "Ctrl+S",
      });
    });

    it("omits shortcuts without a label", () => {
      const handler = vi.fn();
      const shortcut = { key: "x", ctrl: false, shift: false, alt: false, handler };
      registerShortcut(shortcut);
      registered.push(shortcut);
      register("Visible", { key: "y", ctrl: false, shift: false, alt: false });
      const labels = listShortcuts().map((s) => s.label);
      expect(labels).toEqual(["Visible"]);
    });
  });

  describe("event matching with custom overrides", () => {
    it("fires the handler when the custom override is pressed", () => {
      const { handler: saveHandler } = register("Save File", {
        key: "s",
        ctrl: true,
        shift: false,
        alt: false,
      });
      setCustomShortcut("Save File", { key: "k", ctrl: true, shift: false, alt: false });
      // Pressing Ctrl+K (the custom override) should fire the handler.
      window.dispatchEvent(makeKeyEvent("k", { ctrl: true }));
      expect(saveHandler).toHaveBeenCalledTimes(1);
    });

    it("does not fire the handler when the default is pressed after override", () => {
      const { handler: saveHandler } = register("Save File", {
        key: "s",
        ctrl: true,
        shift: false,
        alt: false,
      });
      setCustomShortcut("Save File", { key: "k", ctrl: true, shift: false, alt: false });
      // Pressing Ctrl+S (the old default) should NOT fire the handler.
      window.dispatchEvent(makeKeyEvent("s", { ctrl: true }));
      expect(saveHandler).not.toHaveBeenCalled();
    });

    it("falls back to default after resetCustomShortcut", () => {
      const { handler: saveHandler } = register("Save File", {
        key: "s",
        ctrl: true,
        shift: false,
        alt: false,
      });
      setCustomShortcut("Save File", { key: "k", ctrl: true, shift: false, alt: false });
      resetCustomShortcut("Save File");
      // After reset, Ctrl+S (default) should fire, Ctrl+K should not.
      window.dispatchEvent(makeKeyEvent("k", { ctrl: true }));
      expect(saveHandler).not.toHaveBeenCalled();
      window.dispatchEvent(makeKeyEvent("s", { ctrl: true }));
      expect(saveHandler).toHaveBeenCalledTimes(1);
    });

    it("treats metaKey as ctrl (cross-platform)", () => {
      const { handler: saveHandler } = register("Save File", {
        key: "s",
        ctrl: true,
        shift: false,
        alt: false,
      });
      // Cmd+S on mac should match the Ctrl+S binding.
      window.dispatchEvent(makeKeyEvent("s", { meta: true }));
      expect(saveHandler).toHaveBeenCalledTimes(1);
    });

    it("only fires the first matching shortcut", () => {
      const { handler: h1 } = register("First", { key: "a", ctrl: false, shift: false, alt: false });
      const { handler: h2 } = register("Second", { key: "a", ctrl: false, shift: false, alt: false });
      window.dispatchEvent(makeKeyEvent("a"));
      expect(h1).toHaveBeenCalledTimes(1);
      expect(h2).not.toHaveBeenCalled();
    });

    it("unregisters shortcuts owned by a stopped Vue scope", () => {
      const handler = vi.fn();
      const scope = effectScope();
      scope.run(() => {
        registerShortcut({ key: "q", handler });
      });

      scope.stop();
      window.dispatchEvent(makeKeyEvent("q"));

      expect(handler).not.toHaveBeenCalled();
    });

    it("removes the global listener after the final shortcut is unregistered", () => {
      const removeSpy = vi.spyOn(window, "removeEventListener");
      const shortcut = { key: "z", handler: vi.fn() };
      registerShortcut(shortcut);

      unregisterShortcut(shortcut);

      expect(removeSpy).toHaveBeenCalledWith("keydown", expect.any(Function));
      removeSpy.mockRestore();
    });
  });
});
