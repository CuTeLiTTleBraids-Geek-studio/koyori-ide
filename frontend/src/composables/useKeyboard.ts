// Koyori IDE 模块 · Use Keyboard。
// 喵，这是 Koyori IDE 的 Use Keyboard 模块（前端实现）~
import { getCurrentScope, onMounted, onScopeDispose, onUnmounted } from "vue";
import type { ShortcutKeys } from "@/types";

type ShortcutHandler = (e: KeyboardEvent) => void;

interface Shortcut {
  key: string;
  ctrl?: boolean;
  shift?: boolean;
  alt?: boolean;
  handler: ShortcutHandler;
  preventDefault?: boolean;
  // Human-readable label for the settings UI (#25 / N-8). When omitted,
  // the shortcut won't appear in the shortcuts list. The label also serves
  // as the stable key for customization overrides (N-8).
  label?: string;
}

const shortcuts: Shortcut[] = [];
let listenerInstalled = false;

// N-8: user-customized key bindings, keyed by shortcut label. An entry here
// overrides the Shortcut's default key/ctrl/shift/alt at match time and for
// display. Persists via Settings.customShortcuts.
const customBindings = new Map<string, ShortcutKeys>();

/**
 * Returns the effective key combination for a shortcut: the custom override
 * if present, otherwise the shortcut's default. Used for both event matching
 * and display.
 */
function effectiveKeys(s: Shortcut): ShortcutKeys {
  if (s.label) {
    const custom = customBindings.get(s.label);
    if (custom) return custom;
  }
  return {
    key: s.key,
    ctrl: !!s.ctrl,
    shift: !!s.shift,
    alt: !!s.alt,
  };
}

function keysMatch(k: ShortcutKeys, e: KeyboardEvent): boolean {
  if (k.key.toLowerCase() !== e.key.toLowerCase()) return false;
  if (k.ctrl !== (e.ctrlKey || e.metaKey)) return false;
  if (k.shift !== e.shiftKey) return false;
  if (k.alt !== e.altKey) return false;
  return true;
}

function handleKeyDown(e: KeyboardEvent): void {
  for (const s of shortcuts) {
    if (!keysMatch(effectiveKeys(s), e)) continue;
    if (s.preventDefault !== false) e.preventDefault();
    s.handler(e);
    return;
  }
}

function ensureListener(): void {
  if (listenerInstalled) return;
  window.addEventListener("keydown", handleKeyDown);
  listenerInstalled = true;
}

function removeListenerIfIdle(): void {
  if (shortcuts.length > 0 || !listenerInstalled) return;
  window.removeEventListener("keydown", handleKeyDown);
  listenerInstalled = false;
}

export function registerShortcut(shortcut: Shortcut): void {
  shortcuts.push(shortcut);
  ensureListener();
  if (getCurrentScope()) {
    onScopeDispose(() => unregisterShortcut(shortcut));
  }
}

export function unregisterShortcut(shortcut: Shortcut): void {
  const idx = shortcuts.indexOf(shortcut);
  if (idx >= 0) shortcuts.splice(idx, 1);
  removeListenerIfIdle();
}

/**
 * formatShortcutKey renders a ShortcutKeys as a display string like
 * "Ctrl+Shift+P". Used for both defaults and custom overrides (N-8).
 */
export function formatShortcutKey(k: ShortcutKeys): string {
  const parts: string[] = [];
  if (k.ctrl) parts.push("Ctrl");
  if (k.shift) parts.push("Shift");
  if (k.alt) parts.push("Alt");
  // Normalize single-letter keys to uppercase for display.
  parts.push(k.key.length === 1 ? k.key.toUpperCase() : k.key);
  return parts.join("+");
}

/**
 * listShortcuts returns the currently registered shortcuts that have a label,
 * formatted for display in the settings UI (#25 / N-8). The `keys` field
 * reflects the effective binding (custom override or default), and
 * `isCustom` indicates whether a custom override is active (N-8).
 */
export function listShortcuts(): {
  label: string;
  keys: string;
  isCustom: boolean;
  defaultKeys: string;
}[] {
  return shortcuts
    .filter((s) => s.label)
    .map((s) => {
      const eff = effectiveKeys(s);
      const def: ShortcutKeys = {
        key: s.key,
        ctrl: !!s.ctrl,
        shift: !!s.shift,
        alt: !!s.alt,
      };
      return {
        label: s.label!,
        keys: formatShortcutKey(eff),
        isCustom: !!s.label && customBindings.has(s.label),
        defaultKeys: formatShortcutKey(def),
      };
    });
}

// --- N-8 Customization API ---

/**
 * Sets a custom key binding override for the shortcut with the given label.
 * If the same combo is already used by another shortcut (custom or default),
 * the conflict is reported via the return value; the caller decides whether
 * to proceed. The override is applied regardless so the latest assignment wins.
 */
export function setCustomShortcut(label: string, keys: ShortcutKeys): {
  conflicts: string[];
} {
  customBindings.set(label, { ...keys });
  return { conflicts: findConflicts(label, keys) };
}

/**
 * Removes the custom override for the given label, restoring the default.
 */
export function resetCustomShortcut(label: string): void {
  customBindings.delete(label);
}

/**
 * Removes all custom overrides, restoring every shortcut to its default.
 */
export function resetAllCustomShortcuts(): void {
  customBindings.clear();
}

/**
 * Returns a snapshot of all custom overrides for persistence.
 */
export function getCustomShortcuts(): Record<string, ShortcutKeys> {
  const out: Record<string, ShortcutKeys> = {};
  for (const [label, keys] of customBindings) {
    out[label] = { ...keys };
  }
  return out;
}

/**
 * Replaces all custom overrides from a persisted snapshot. Called during
 * settings load. Entries whose label doesn't match a registered shortcut
 * are still stored (they'll take effect if the shortcut is registered later).
 */
export function loadCustomShortcuts(map?: Record<string, ShortcutKeys> | null): void {
  customBindings.clear();
  if (map) {
    for (const [label, keys] of Object.entries(map)) {
      customBindings.set(label, { ...keys });
    }
  }
}

/**
 * Returns true if the given label has a custom override active.
 */
export function hasCustomShortcut(label: string): boolean {
  return customBindings.has(label);
}

/**
 * Finds labels of other shortcuts whose effective binding matches the given
 * keys. Used by the settings UI to warn about conflicts before applying.
 * Excludes the given label from the results.
 */
export function findConflicts(label: string, keys: ShortcutKeys): string[] {
  const conflicts: string[] = [];
  for (const s of shortcuts) {
    if (!s.label || s.label === label) continue;
    const eff = effectiveKeys(s);
    if (
      eff.key.toLowerCase() === keys.key.toLowerCase() &&
      eff.ctrl === keys.ctrl &&
      eff.shift === keys.shift &&
      eff.alt === keys.alt
    ) {
      conflicts.push(s.label);
    }
  }
  return conflicts;
}

export function useKeyboard(): void {
  onMounted(() => {
    ensureListener();
  });
  onUnmounted(() => {
    removeListenerIfIdle();
  });
}
