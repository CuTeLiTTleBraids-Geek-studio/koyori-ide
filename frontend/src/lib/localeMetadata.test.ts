import { describe, it, expect } from "vitest";
import {
  pluralCategories,
  isRtlLocale,
  normalizeLocale,
} from "./localeMetadata";

describe("localeMetadata (G25)", () => {
  it("normalizes locale tags", () => {
    expect(normalizeLocale("en-US")).toBe("en-us");
    expect(normalizeLocale("ZH_CN")).toBe("zh-cn");
    expect(normalizeLocale("ru")).toBe("ru");
    expect(normalizeLocale("ja_JP")).toBe("ja-jp");
  });

  it("reports the plural categories for the packaged matrix", () => {
    expect(pluralCategories("en")).toEqual(["one", "other"]);
    expect(pluralCategories("en-us")).toEqual(["one", "other"]);
    expect(pluralCategories("zh")).toEqual(["other"]);
    expect(pluralCategories("zh-cn")).toEqual(["other"]);
    expect(pluralCategories("ja")).toEqual(["other"]);
  });

  it("reports complex plural languages (ru/pl/ar)", () => {
    const ru = pluralCategories("ru");
    expect(ru).toContain("one");
    expect(ru).toContain("few");
    expect(ru).toContain("many");
    expect(ru).toContain("other");

    const pl = pluralCategories("pl");
    expect(pl).toContain("one");
    expect(pl).toContain("few");
    expect(pl).toContain("many");

    const ar = pluralCategories("ar");
    expect(ar).toContain("zero");
    expect(ar).toContain("one");
    expect(ar).toContain("two");
    expect(ar).toContain("few");
    expect(ar).toContain("many");
  });

  it("detects RTL locales", () => {
    expect(isRtlLocale("ar")).toBe(true);
    expect(isRtlLocale("he-IL")).toBe(true);
    expect(isRtlLocale("fa")).toBe(true);
    expect(isRtlLocale("ur-PK")).toBe(true);
    expect(isRtlLocale("en")).toBe(false);
    expect(isRtlLocale("zh")).toBe(false);
    expect(isRtlLocale("ru")).toBe(false);
  });

  it("falls back to platform ICU for unknown locales", () => {
    // "fr" is not in the static table but has real ICU rules; the function
    // must return at least one/other without throwing.
    const fr = pluralCategories("fr");
    expect(fr.length).toBeGreaterThan(0);
    expect(fr).toContain("one");
    expect(fr).toContain("other");
  });

  it("falls back to other-only when Intl is unavailable", () => {
    // Simulate an environment without Intl.PluralRules by temporarily
    // replacing it; the static table paths still resolve.
    const original = globalThis.Intl.PluralRules;
    try {
      // @ts-expect-error — deliberate runtime mutation for the fallback test
      globalThis.Intl.PluralRules = undefined;
      // Unknown locale with no Intl: must degrade to ["other"].
      const degraded = pluralCategories("zz-zz");
      expect(degraded).toEqual(["other"]);
      // Known static locales still resolve without Intl.
      expect(pluralCategories("en")).toEqual(["one", "other"]);
    } finally {
      // @ts-expect-error — restore the real constructor.
      globalThis.Intl.PluralRules = original;
    }
  });
});
