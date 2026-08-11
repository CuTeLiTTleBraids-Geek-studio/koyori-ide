/**
 * prompt-6 Task 1 — per-webview origin id for dual-window sync.
 *
 * Main editor and AI companion are independent Webviews. Events such as
 * settings:changed / conversation:saved are broadcast app-wide; each window
 * tags its own emissions with this id and ignores echoes of itself to avoid
 * reload loops.
 */
// Koyori IDE 模块 · Window Origin。
// 喵，这是 Koyori IDE 的 Window Origin 模块（前端实现）~
const STORAGE_KEY = "koyori-ide.windowOriginId";

function createOriginId(): string {
  const rand =
    typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
      ? crypto.randomUUID()
      : `${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 10)}`;
  return `win_${rand}`;
}

/** Stable origin id for the current Webview process (sessionStorage when available). */
export function getWindowOriginId(): string {
  try {
    if (typeof sessionStorage !== "undefined") {
      const existing = sessionStorage.getItem(STORAGE_KEY);
      if (existing) return existing;
      const id = createOriginId();
      sessionStorage.setItem(STORAGE_KEY, id);
      return id;
    }
  } catch {
    // sessionStorage may throw in private mode / tests
  }
  // L-10: Fallback for jsdom / tests without sessionStorage. Previously
  // this cached the id on globalThis, which leaked across instances
  // (multiple callers in the same JS runtime would share one id). Now
  // we generate a fresh id each call so there is no cross-contamination.
  return createOriginId();
}

/** Unwrap Wails event data (may be raw value, {data}, or array-wrapped). */
export function unwrapEventData(event: unknown): unknown {
  if (event == null) return event;
  if (typeof event === "object" && event !== null && "data" in event) {
    const raw = (event as { data?: unknown }).data;
    return Array.isArray(raw) ? raw[0] : raw;
  }
  return Array.isArray(event) ? event[0] : event;
}

export function parseSyncOrigin(payload: unknown): string {
  if (payload && typeof payload === "object" && "origin" in payload) {
    const o = (payload as { origin?: unknown }).origin;
    return typeof o === "string" ? o : "";
  }
  return "";
}
