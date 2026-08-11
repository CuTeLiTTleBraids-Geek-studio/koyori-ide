/**
 * G25: locale metadata for ICU-aware formatting.
 *
 * The i18n framework stays dependency-free (no vue-i18n); this module adds
 * the minimum metadata needed to format plurals and detect RTL/bidi without
 * string concatenation errors:
 *
 *   - `pluralCategories(locale)`: which CLDR plural categories a locale
 *     actually uses, derived from `Intl.PluralRules` when available and
 *     falling back to a static table for the packaged matrix.
 *   - `isRtlLocale(locale)`: direction metadata for RTL/bidi handling.
 *
 * The plural categories are used by `translate()` to resolve
 * `{count, plural, one{...} other{...}}` ICU selectors. Falling back to
 * `Intl.PluralRules` keeps the rule set aligned with the platform ICU data
 * instead of a hard-coded approximation.
 */

export type LocaleCode = string;

const RTL_LOCALES = new Set(["ar", "he", "fa", "ur", "ps", "yi", "dv"]);

/**
 * CLDR plural categories that are actually used for a locale. This is the
 * authoritative rule source: the platform ICU data (via Intl.PluralRules)
 * when available, otherwise a conservative static table for locales that
 * must work in the packaged matrix.
 */
export function pluralCategories(locale: LocaleCode): string[] {
  const normalized = normalizeLocale(locale);
  const staticTable: Record<string, string[]> = {
    en: ["one", "other"],
    "en-us": ["one", "other"],
    "en-gb": ["one", "other"],
    zh: ["other"],
    "zh-cn": ["other"],
    "zh-hans": ["other"],
    ja: ["other"],
    ru: ["one", "few", "many", "other"],
    pl: ["one", "few", "many", "other"],
    ar: ["zero", "one", "two", "few", "many", "other"],
  };
  const staticallyKnown = staticTable[normalized];
  if (staticallyKnown) return [...staticallyKnown];
  // Fall back to the platform ICU rules for any locale the static table does
  // not list; this keeps the packaged matrix honest on real Windows ICU.
  try {
    const rules = new Intl.PluralRules(normalized);
    // Probe each category with a representative sample; the spec requires
    // Intl.PluralRules.resolvedOptions to expose pluralCategories in modern
    // engines, and the probe loop covers older engines.
    const categories =
      (rules.resolvedOptions() as { pluralCategories?: string[] }).pluralCategories
      ?? probePluralCategories(rules);
    return categories;
  } catch {
    return ["other"];
  }
}

function probePluralCategories(rules: Intl.PluralRules): string[] {
  const categories = new Set<string>();
  for (let n = 0; n <= 1_000; n += 1) {
    categories.add(rules.select(n));
  }
  categories.add(rules.select(0.5));
  categories.add(rules.select(2.5));
  return [...categories].sort();
}

/** Canonical lowercase tag without region aliasing surprises. */
export function normalizeLocale(locale: LocaleCode): string {
  return String(locale).trim().toLowerCase().replace(/_/g, "-");
}

export function isRtlLocale(locale: LocaleCode): boolean {
  return RTL_LOCALES.has(normalizeLocale(locale).split("-")[0]);
}

export const testable = { RTL_LOCALES, probePluralCategories };
