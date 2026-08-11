import { describe, it, expect, beforeEach, vi } from "vitest";

// Mock the heavy monaco-themes module so importing @/stores/app (which
// i18n.ts depends on) does not pull in monaco-editor, which fails under
// jsdom (document.queryCommandSupported is missing).
vi.mock("@/lib/monaco-themes", () => ({
  accentThemes: {
    blue: { label: "Blue", color: "#4285f4", monacoTheme: "koyoriIde-blue", monacoLightTheme: "koyoriIde-light-blue" },
  },
  applyMonacoTheme: vi.fn(),
  applyMonacoThemeForMode: vi.fn(),
  registerAllThemes: vi.fn(),
}));

vi.mock("@/api/services", () => ({
  settingsService: {
    loadSettings: vi.fn().mockResolvedValue({}),
    saveSettings: vi.fn().mockResolvedValue(undefined),
  },
}));

vi.mock("@wailsio/runtime", () => ({
  Events: { On: vi.fn() },
}));

import { appState } from "@/stores/app";
import {
  translate,
  getCurrentLocale,
  useI18n,
  __setLocaleDictionary,
  __resetLocaleDictionary,
  __getLocaleDictionary,
} from "./i18n";

describe("i18n", () => {
  beforeEach(() => {
    appState.language = "en";
  });

  describe("getCurrentLocale", () => {
    it("returns 'en' by default", () => {
      appState.language = "en";
      expect(getCurrentLocale()).toBe("en");
    });

    it("returns 'zh' when language is 'zh'", () => {
      appState.language = "zh";
      expect(getCurrentLocale()).toBe("zh");
    });

    it("returns 'ja' when language is 'ja'", () => {
      appState.language = "ja";
      expect(getCurrentLocale()).toBe("ja");
    });

    it("falls back to 'en' for unknown language codes", () => {
      appState.language = "fr";
      expect(getCurrentLocale()).toBe("en");
    });

    it("falls back to 'en' for empty string", () => {
      appState.language = "";
      expect(getCurrentLocale()).toBe("en");
    });
  });

  describe("translate", () => {
    it("returns English value when locale is 'en'", () => {
      appState.language = "en";
      expect(translate("activity.explorer")).toBe("Explorer");
    });

    it("returns Chinese value when locale is 'zh'", () => {
      appState.language = "zh";
      expect(translate("activity.explorer")).toBe("资源管理器");
    });

    it("returns Japanese value when locale is 'ja'", () => {
      appState.language = "ja";
      expect(translate("activity.explorer")).toBe("エクスプローラー");
    });

    it("falls back to English when key is missing in current locale", () => {
      appState.language = "zh";
      // 'common.loading' exists in all locales, so we test with a fake key
      // by registering a partial zh dictionary.
      __setLocaleDictionary("zh", { "activity.explorer": "资源管理器" });
      try {
        expect(translate("common.loading")).toBe("Loading...");
      } finally {
        __resetLocaleDictionary("zh");
      }
    });

    it("returns the key itself when missing from all locales", () => {
      expect(translate("nonexistent.key.something")).toBe("nonexistent.key.something");
    });

    it("interpolates {placeholder} params", () => {
      appState.language = "en";
      expect(translate("general.logReadFailed", { error: "disk full" })).toBe(
        "Failed to read log: disk full",
      );
    });

    it("interpolates params in Chinese", () => {
      appState.language = "zh";
      expect(translate("general.logReadFailed", { error: "磁盘已满" })).toBe(
        "读取日志失败：磁盘已满",
      );
    });

    it("interpolates numeric params", () => {
      appState.language = "en";
      // Use a synthetic key with a placeholder via the test helper.
      __setLocaleDictionary("en", { "test.count": "Count: {n}" });
      try {
        expect(translate("test.count", { n: 42 })).toBe("Count: 42");
      } finally {
        __resetLocaleDictionary("en");
      }
    });

    it("handles multiple occurrences of the same placeholder", () => {
      appState.language = "en";
      __setLocaleDictionary("en", { "test.dup": "{x} and {x} again" });
      try {
        expect(translate("test.dup", { x: "A" })).toBe("A and A again");
      } finally {
        __resetLocaleDictionary("en");
      }
    });

    it("returns empty string for empty key", () => {
      expect(translate("")).toBe("");
    });

    it("escapes regex metacharacters in placeholder names", () => {
      appState.language = "en";
      __setLocaleDictionary("en", { "test.meta": "Value: {na.me}" });
      try {
        expect(translate("test.meta", { "na.me": "ok" })).toBe("Value: ok");
      } finally {
        __resetLocaleDictionary("en");
      }
    });
  });

  describe("useI18n", () => {
    it("returns a t function that translates", () => {
      appState.language = "en";
      const { t } = useI18n();
      expect(t("activity.search")).toBe("Search");
    });

    it("returns a locale computed that reflects current language", () => {
      appState.language = "zh";
      const { locale } = useI18n();
      expect(locale.value).toBe("zh");
    });

    it("locale computed updates when language changes", () => {
      appState.language = "en";
      const { locale } = useI18n();
      expect(locale.value).toBe("en");
      appState.language = "ja";
      expect(locale.value).toBe("ja");
    });

    it("t function passes params through to translate", () => {
      appState.language = "en";
      const { t } = useI18n();
      expect(t("general.logReadFailed", { error: "boom" })).toBe(
        "Failed to read log: boom",
      );
    });
  });

  describe("translation coverage", () => {
    // Proposal AI: Automated key-parity check. Instead of a manually
    // maintained list (which drifts), we iterate ALL keys in the English
    // dictionary and verify each one exists in zh and ja. This catches
    // missing translations automatically when new keys are added.

    const enDict = __getLocaleDictionary("en");
    const zhDict = __getLocaleDictionary("zh");
    const jaDict = __getLocaleDictionary("ja");
    const enKeys = Object.keys(enDict).sort();

    it("English dictionary is non-empty", () => {
      expect(enKeys.length).toBeGreaterThan(100);
    });

    it("Chinese dictionary has every key in the English dictionary", () => {
      const missing = enKeys.filter((k) => !(k in zhDict));
      if (missing.length > 0) {
        // Print all missing keys so the developer knows exactly what to add
        console.error("Missing Chinese keys:", missing);
      }
      expect(missing).toEqual([]);
    });

    it("Japanese dictionary has every key in the English dictionary", () => {
      const missing = enKeys.filter((k) => !(k in jaDict));
      if (missing.length > 0) {
        console.error("Missing Japanese keys:", missing);
      }
      expect(missing).toEqual([]);
    });

    it("no locale has extra keys absent from English", () => {
      // Keys in zh/ja but not in en are likely typos or stale leftovers
      const zhExtra = Object.keys(zhDict).filter((k) => !(k in enDict));
      const jaExtra = Object.keys(jaDict).filter((k) => !(k in enDict));
      expect(zhExtra).toEqual([]);
      expect(jaExtra).toEqual([]);
    });

    it("no empty-string values in any locale (except explicitly allowed)", () => {
      // Empty values indicate a translator forgot to fill in a string.
      // The only exception is agentSection.warningPrefix when auto-approve
      // is off — but that is handled via conditional rendering, not an
      // empty i18n value. All i18n values should be non-empty.
      const emptyZh = enKeys.filter((k) => zhDict[k] === "");
      const emptyJa = enKeys.filter((k) => jaDict[k] === "");
      if (emptyZh.length > 0) console.error("Empty Chinese values:", emptyZh);
      if (emptyJa.length > 0) console.error("Empty Japanese values:", emptyJa);
      expect(emptyZh).toEqual([]);
      expect(emptyJa).toEqual([]);
    });

    it("multi-line prompt strings have matching section headers across locales", () => {
      // Catches truncated prompts (N-119). For each prompts.* key, extract
      // the "# Section" headers and verify they match across all locales.
      const promptKeys = enKeys.filter((k) => k.startsWith("prompts."));
      for (const key of promptKeys) {
        const extractHeaders = (s: string): string[] => {
          const headers: string[] = [];
          for (const line of s.split("\n")) {
            const m = line.match(/^#+\s*.+/);
            if (m) headers.push(m[0].trim());
          }
          return headers;
        };
        const enHeaders = extractHeaders(enDict[key]);
        const zhHeaders = extractHeaders(zhDict[key]);
        const jaHeaders = extractHeaders(jaDict[key]);
        // Headers should match in count (translations may use different
        // text but the section structure must be identical)
        expect(zhHeaders.length, `${key} zh section count`).toBe(enHeaders.length);
        expect(jaHeaders.length, `${key} ja section count`).toBe(enHeaders.length);
      }
    });

    it("Chinese translations differ from English (sanity check)", () => {
      appState.language = "en";
      const enValue = translate("activity.explorer");
      appState.language = "zh";
      const zhValue = translate("activity.explorer");
      expect(zhValue).not.toBe(enValue);
    });

    it("Japanese translations differ from English (sanity check)", () => {
      appState.language = "en";
      const enValue = translate("activity.explorer");
      appState.language = "ja";
      const jaValue = translate("activity.explorer");
      expect(jaValue).not.toBe(enValue);
    });
  });

  // N-59: Localized AI system prompts
  describe("AI prompt localization (N-59)", () => {
    it("prompts.defaultSystem exists in English", () => {
      appState.language = "en";
      const value = translate("prompts.defaultSystem");
      expect(value).not.toBe("prompts.defaultSystem");
      expect(value.length).toBeGreaterThan(100);
      // The product name is "Koyori IDE" (README/version metadata); match it
      // case-insensitively so a "Koyori IDE" or "koyori-ide" spelling both pass.
      expect(value).toMatch(/koyori[- ]?ide/i);
    });

    it("prompts.defaultSystem exists in Chinese", () => {
      appState.language = "zh";
      const value = translate("prompts.defaultSystem");
      expect(value).not.toBe("prompts.defaultSystem");
      expect(value.length).toBeGreaterThan(50);
    });

    it("prompts.defaultSystem exists in Japanese", () => {
      appState.language = "ja";
      const value = translate("prompts.defaultSystem");
      expect(value).not.toBe("prompts.defaultSystem");
      expect(value.length).toBeGreaterThan(50);
    });

    it("prompts.agentSystem exists in all locales", () => {
      for (const lang of ["en", "zh", "ja"] as const) {
        appState.language = lang;
        const value = translate("prompts.agentSystem");
        expect(value).not.toBe("prompts.agentSystem");
        expect(value.length).toBeGreaterThan(50);
      }
    });

    it("prompts.conversationTitle has {{first_message}} placeholder in all locales", () => {
      for (const lang of ["en", "zh", "ja"] as const) {
        appState.language = lang;
        const value = translate("prompts.conversationTitle");
        expect(value).toContain("{{first_message}}");
      }
    });

    it("prompts.inlineCompletion has {{language}} placeholder in all locales", () => {
      for (const lang of ["en", "zh", "ja"] as const) {
        appState.language = lang;
        const value = translate("prompts.inlineCompletion");
        expect(value).toContain("{{language}}");
      }
    });

    it("Chinese default prompt differs from English", () => {
      appState.language = "en";
      const en = translate("prompts.defaultSystem");
      appState.language = "zh";
      const zh = translate("prompts.defaultSystem");
      expect(zh).not.toBe(en);
    });

    it("Japanese default prompt differs from English", () => {
      appState.language = "en";
      const en = translate("prompts.defaultSystem");
      appState.language = "ja";
      const ja = translate("prompts.defaultSystem");
      expect(ja).not.toBe(en);
    });
  });
});
