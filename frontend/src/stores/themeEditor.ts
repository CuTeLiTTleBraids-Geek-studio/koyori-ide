/**
 * Theme editor utilities (Plan 57 / N-23). Extracted from app.ts to give
 * the custom-accent logic a clear module boundary.
 *
 * The pure functions (deriveAccentTokens, serializeCustomAccent,
 * deserializeCustomAccent) and the DOM side-effect helpers
 * (applyCustomAccentTokens, clearCustomAccentTokens) live here.
 *
 * setCustomAccent stays in app.ts because it depends on appState,
 * registerCustomTheme, applyMonacoTheme, and saveSettings — all of which
 * are defined in app.ts.
 */
// Koyori IDE 模块 · Theme Editor。
// 喵，这是 Koyori IDE 的 Theme Editor 模块（前端实现）~

import type { CustomAccentTheme } from "@/types";

/**
 * The accent CSS tokens that are overridden per accent theme. Used by
 * applyCustomAccentTokens / clearCustomAccentTokens to keep the set in sync.
 */
export const ACCENT_TOKENS = [
  "--color-primary",
  "--color-primary-hover",
  "--color-primary-light",
  "--color-primary-container",
  "--color-on-primary",
  "--color-on-primary-container",
  "--color-tertiary",
  "--el-color-primary",
] as const;

/**
 * Derive the accent token values from a base color. Follows the same
 * alpha-suffix pattern used by the Monaco theme generators. Overrides take
 * precedence over derived values.
 */
export function deriveAccentTokens(
  custom: CustomAccentTheme,
): Record<string, string> {
  const c = custom.color;
  return {
    "--color-primary": custom.primary ?? c,
    "--color-primary-hover": custom.primaryHover ?? c + "ee",
    "--color-primary-light": custom.primaryLight ?? c + "30",
    "--color-primary-container": custom.primaryContainer ?? c + "20",
    "--color-on-primary": custom.onPrimary ?? "#ffffff",
    "--color-on-primary-container": custom.onPrimaryContainer ?? c,
    "--color-tertiary": custom.primaryLight ?? c + "30",
    "--el-color-primary": custom.primary ?? c,
  };
}

/**
 * Apply custom accent tokens as inline CSS variables on <html>. Also sets
 * data-theme="custom" so any CSS that targets [data-theme="custom"] applies.
 */
export function applyCustomAccentTokens(custom: CustomAccentTheme): void {
  const root = document.documentElement;
  root.setAttribute("data-theme", "custom");
  const tokens = deriveAccentTokens(custom);
  for (const [prop, value] of Object.entries(tokens)) {
    root.style.setProperty(prop, value);
  }
}

/**
 * Remove all custom accent inline CSS variables so the built-in accent
 * (set via data-theme attribute) takes effect again.
 */
export function clearCustomAccentTokens(): void {
  const root = document.documentElement;
  for (const prop of ACCENT_TOKENS) {
    root.style.removeProperty(prop);
  }
}

/**
 * Serialize a custom accent theme to a JSON string for export.
 */
export function serializeCustomAccent(custom: CustomAccentTheme): string {
  return JSON.stringify(custom, null, 2);
}

/**
 * Deserialize a JSON string to a CustomAccentTheme. Throws on invalid JSON
 * or missing required fields (name, color).
 */
export function deserializeCustomAccent(json: string): CustomAccentTheme {
  const obj = JSON.parse(json) as Record<string, unknown>;
  if (typeof obj.name !== "string" || typeof obj.color !== "string") {
    throw new Error("Invalid theme: name and color are required strings");
  }
  if (!/^#[0-9a-fA-F]{6}$/.test(obj.color)) {
    throw new Error(`Invalid theme color: ${obj.color}. Expected hex like #ff6b35`);
  }
  return {
    name: obj.name,
    color: obj.color,
    primary: typeof obj.primary === "string" ? obj.primary : undefined,
    primaryHover: typeof obj.primaryHover === "string" ? obj.primaryHover : undefined,
    primaryLight: typeof obj.primaryLight === "string" ? obj.primaryLight : undefined,
    primaryContainer: typeof obj.primaryContainer === "string" ? obj.primaryContainer : undefined,
    onPrimary: typeof obj.onPrimary === "string" ? obj.onPrimary : undefined,
    onPrimaryContainer: typeof obj.onPrimaryContainer === "string" ? obj.onPrimaryContainer : undefined,
  };
}
