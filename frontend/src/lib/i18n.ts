// Koyori IDE 模块 · I18N。
// 喵，这是 Koyori IDE 的 I18N 模块（前端实现）~
import { computed } from "vue";
import { appState } from "@/stores/app";
import { pluralCategories } from "@/lib/localeMetadata";

/**
 * Minimal i18n framework (N-12, G25).
 *
 * Why not vue-i18n? The project is a desktop IDE with a fixed, finite set
 * of UI strings. vue-i18n would add a heavy dependency and a global plugin
 * that complicates testing. A purpose-built module with the same reactive
 * contract (read appState.language inside template-rendered functions) is
 * simpler, type-safe, and ~80 lines.
 *
 * Reactivity: `t()` reads `appState.language` in its body, so any template
 * that calls `t('foo')` re-renders when the language changes. `useI18n()`
 * additionally exposes a `locale` computed for components that need to
 * branch on the current language.
 *
 * G25: `translate()` now supports ICU plural selectors of the shape
 * `{count, plural, one{...} few{...} other{...}}`. Plural categories are
 * resolved through `localeMetadata.pluralCategories` (platform ICU when
 * available). Missing keys are counted and warn through
 * `__getMissingKeyCount()` so test suites can monitor coverage instead of
 * silently rendering raw keys.
 */

export type Locale = "en" | "zh" | "ja";

export type MessageDict = Record<string, string>;

import en from "./locales/en";
import zh from "./locales/zh";
import ja from "./locales/ja";

const dictionaries: Record<Locale, MessageDict> = { en, zh, ja };

// G25: missing-key monitor. Incremented for every key that is absent from
// every dictionary (raw-key fallback). Reset between test runs.
let missingKeyCount = 0;

/**
 * Get the active locale, falling back to "en" for unknown values.
 */
export function getCurrentLocale(): Locale {
  const lang = appState.language;
  if (lang === "zh" || lang === "ja") return lang;
  return "en";
}

/** Test-only: read how many keys fell back to the raw key since the last reset. */
export function __getMissingKeyCount(): number {
  return missingKeyCount;
}

/** Test-only: reset the missing-key monitor (used in tests, never in templates). */
export function __resetMissingKeyCount(): void {
  missingKeyCount = 0;
}

const ICU_PLURAL_OPEN_RE = /\{([A-Za-z0-9_]+),\s*plural,/;
const ICU_PLURAL_CLOSE_RE = /\{([A-Za-z0-9_]+),\s*plural,\s*([^{}]*(?:\{[^{}]*\}[^{}]*)*)\}/;
const ICU_PLURAL_SEGMENT_RE = /([a-z]+)\s*\{([^{}]*)\}/g;

/**
 * Resolve an ICU plural selector:
 *   "{count, plural, one{# file} other{# files}}"
 * Chooses the category via `Intl.PluralRules(locale).select(count)`; unknown
 * categories in the selector are ignored, and "other" is the final fallback.
 *
 * Exported for direct testing of complex-plural locales (ru/pl/ar) that are
 * not reachable through the app language selector, which currently offers
 * only en/zh/ja.
 */
export function resolveIcuPlural(
  selector: string,
  count: unknown,
  locale: string,
): string {
  const closeMatch = selector.match(ICU_PLURAL_CLOSE_RE);
  if (!closeMatch) return selector;
  const variable = closeMatch[1];
  const segmentsText = closeMatch[2];
  const numeric = typeof count === "number"
    ? count
    : typeof count === "string" && count.trim() !== "" && Number.isFinite(Number(count))
      ? Number(count)
      : NaN;
  const segments = new Map<string, string>();
  ICU_PLURAL_SEGMENT_RE.lastIndex = 0;
  let segmentMatch: RegExpExecArray | null;
  while ((segmentMatch = ICU_PLURAL_SEGMENT_RE.exec(segmentsText)) !== null) {
    segments.set(segmentMatch[1], segmentMatch[2]);
  }
  // Ask the platform ICU which category the actual count belongs to, then
  // fall back through the locale's declared categories to "other".
  let actualCategory = "other";
  if (!Number.isNaN(numeric)) {
    try {
      actualCategory = new Intl.PluralRules(locale).select(numeric);
    } catch {
      actualCategory = numeric === 1 ? "one" : "other";
    }
  }
  let chosen = segments.has(actualCategory)
    ? actualCategory
    : "other";
  if (!segments.has(chosen)) {
    // The message omits the actual category; pick the first declared
    // category present in the message as a stable fallback.
    for (const category of pluralCategories(locale)) {
      if (segments.has(category)) {
        chosen = category;
        break;
      }
    }
  }
  const selected = segments.get(chosen) ?? segments.get("other") ?? selector;
  // Replace the `#` count placeholder with the formatted number. Function
  // replacer: a formatted number must never be parsed as `$` replacement
  // syntax (e.g. a locale that formats with a currency-ish suffix).
  const formatted = Number.isNaN(numeric)
    ? String(count ?? "")
    : formatNumber(numeric, locale);
  const result = selected.replace(/#/g, () => formatted);
  // Replace the outer selector with the resolved text, then substitute any
  // remaining {variable} placeholders in the message (literal replacer).
  const countText = String(count ?? "");
  return result.replace(
    new RegExp(`\\{${variable}\\}`, "g"),
    () => countText,
  );
}

/** Locale-aware number formatting (ICU) with a plain fallback. */
export function formatNumber(value: number, locale: string = getCurrentLocale()): string {
  try {
    return new Intl.NumberFormat(locale).format(value);
  } catch {
    return String(value);
  }
}

/**
 * Look up a translation key in the current locale's dictionary, falling
 * back to English, then to the key itself (so missing keys are visible
 * but never crash the UI). Supports `{name}` placeholder interpolation and
 * ICU plural selectors (`{count, plural, ...}`).
 *
 * Reading `appState.language` (via getCurrentLocale) inside this function
 * is what makes template calls reactive — Vue tracks the read during
 * render and re-renders on change.
 */
export function translate(
  key: string,
  params?: Record<string, string | number>,
): string {
  const locale = getCurrentLocale();
  const dict = dictionaries[locale] || en;
  let value = dict[key] ?? en[key];
  if (value === undefined) {
    missingKeyCount += 1;
    return key;
  }
  if (params) {
    // First resolve ICU plural selectors, which may span the whole value.
    const icuOpen = value.match(ICU_PLURAL_OPEN_RE);
    if (icuOpen) {
      const variable = icuOpen[1];
      const count = params[variable];
      const closeMatch = value.match(ICU_PLURAL_CLOSE_RE);
      if (closeMatch) {
        const resolved = resolveIcuPlural(closeMatch[0], count, locale);
        // Function replacer: avoids interpreting `$&`/`$1` inside the resolved
        // text as replacement patterns (String.replace template injection).
        value = value.replace(closeMatch[0], () => resolved);
      }
    }
    for (const [k, v] of Object.entries(params)) {
      // Escape regex metacharacters in the placeholder name (defensive —
      // keys are static strings in practice).
      const safe = k.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
      const replacement = String(v);
      // Function replacer so a value containing `$&`, `$1`, or `$$` is
      // inserted literally instead of being parsed as a replacement pattern.
      value = value.replace(new RegExp(`\\{${safe}\\}`, "g"), () => replacement);
    }
  }
  return value;
}

/**
 * Composable for components that need a reactive `t` function or the
 * current `locale` as a computed ref.
 *
 * Usage in <script setup>:
 *   const { t, locale } = useI18n();
 *   // In template: {{ t('activity.explorer') }}
 */
export function useI18n() {
  const t = (key: string, params?: Record<string, string | number>) =>
    translate(key, params);
  const locale = computed(() => getCurrentLocale());
  return { t, locale };
}

/**
 * Test-only helper: register an additional locale dictionary at runtime.
 * Used by the i18n tests to verify fallback behavior with a fake locale.
 * Not exported through the public surface; tests import this directly.
 */
export function __setLocaleDictionary(locale: Locale, dict: MessageDict): void {
  dictionaries[locale] = dict;
}

/**
 * Test-only helper: reset a locale dictionary to its original value.
 * Implemented by re-assigning the built-in dictionary reference.
 */
export function __resetLocaleDictionary(locale: Locale): void {
  if (locale === "en") dictionaries.en = en;
  else if (locale === "zh") dictionaries.zh = zh;
  else if (locale === "ja") dictionaries.ja = ja;
}

/**
 * Test-only helper: get a read-only reference to a locale's dictionary.
 * Used by the i18n completeness test to verify key parity across locales
 * (Proposal AI — catches missing/truncated translations automatically).
 */
export function __getLocaleDictionary(locale: Locale): MessageDict {
  return dictionaries[locale];
}
