import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

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
  resolveIcuPlural,
  __setLocaleDictionary,
  __resetLocaleDictionary,
  __resetMissingKeyCount,
  __getMissingKeyCount,
} from "./i18n";

describe("i18n G25 ICU plural", () => {
  beforeEach(() => {
    appState.language = "en";
    __resetMissingKeyCount();
    __setLocaleDictionary("en", {
      "test.plural": "{count, plural, one{# file} other{# files}}",
      "test.zero": "{count, plural, zero{# files} one{# file} other{# files}}",
      "test.unknown": "{count, plural, few{# files} other{# files}}",
      "test.mixed": "Found {count, plural, one{# file} other{# files}} in {folder}",
    });
    __setLocaleDictionary("zh", {
      "test.plural": "共 {count} 个文件",
    });
  });

  afterEach(() => {
    __resetLocaleDictionary("en");
    __resetLocaleDictionary("zh");
  });

  it("resolves one/other plural for English", () => {
    appState.language = "en";
    expect(translate("test.plural", { count: 1 })).toBe("1 file");
  });

  it("resolves other plural for English with count > 1", () => {
    appState.language = "en";
    expect(translate("test.plural", { count: 5 })).toBe("5 files");
  });

  it("ignores the zero category for English (falls back to other)", () => {
    appState.language = "en";
    expect(translate("test.zero", { count: 0 })).toBe("0 files");
  });

  it("falls back to other for a category the locale does not use", () => {
    appState.language = "en";
    expect(translate("test.unknown", { count: 3 })).toBe("3 files");
  });

  it("substitutes sibling placeholders after plural resolution", () => {
    appState.language = "en";
    expect(translate("test.mixed", { count: 2, folder: "src" })).toBe(
      "Found 2 files in src",
    );
  });

  it("resolves Russian few/many categories through ICU rules", () => {
    // The app language selector only offers en/zh/ja, so complex-plural
    // locales are exercised directly through resolveIcuPlural with the real
    // locale tag. Russian: 1 -> one, 2 -> few, 5 -> many.
    const ruMessage = "{count, plural, one{# файл} few{# файла} many{# файлов} other{# файлов}}";
    expect(resolveIcuPlural(ruMessage, 1, "ru")).toBe("1 файл");
    expect(resolveIcuPlural(ruMessage, 2, "ru")).toBe("2 файла");
    expect(resolveIcuPlural(ruMessage, 5, "ru")).toBe("5 файлов");
    expect(resolveIcuPlural(ruMessage, 21, "ru")).toBe("21 файл");
  });

  it("resolves Polish few/many categories through ICU rules", () => {
    const plMessage = "{count, plural, one{# plik} few{# pliki} many{# plików} other{# plików}}";
    expect(resolveIcuPlural(plMessage, 1, "pl")).toBe("1 plik");
    expect(resolveIcuPlural(plMessage, 2, "pl")).toBe("2 pliki");
    expect(resolveIcuPlural(plMessage, 5, "pl")).toBe("5 plików");
  });

  it("resolves Arabic zero/two categories through ICU rules", () => {
    const arMessage = "{count, plural, zero{# ملفات} one{# ملف} two{# ملفان} few{# ملفات} many{# ملفًا} other{# ملف}}";
    expect(resolveIcuPlural(arMessage, 0, "ar")).toBe("0 ملفات");
    expect(resolveIcuPlural(arMessage, 1, "ar")).toBe("1 ملف");
    expect(resolveIcuPlural(arMessage, 2, "ar")).toBe("2 ملفان");
    expect(resolveIcuPlural(arMessage, 11, "ar")).toBe("11 ملفًا");
  });

  it("does not interpret $ replacement syntax inside plural or params", () => {
    appState.language = "en";
    __setLocaleDictionary("en", {
      "test.dollar": "Total: {amount}",
      "test.pluralDollar": "{count, plural, one{# file} other{# files}} for {user}",
    });
    try {
      expect(translate("test.dollar", { amount: "$& and $1" })).toBe(
        "Total: $& and $1",
      );
      expect(translate("test.pluralDollar", { count: 2, user: "$&" })).toBe(
        "2 files for $&",
      );
    } finally {
      __resetLocaleDictionary("en");
    }
  });

  it("returns the raw key and increments the monitor for missing keys", () => {
    appState.language = "en";
    expect(__getMissingKeyCount()).toBe(0);
    expect(translate("definitely.missing.g25")).toBe("definitely.missing.g25");
    expect(__getMissingKeyCount()).toBe(1);
    expect(translate("definitely.missing.g25")).toBe("definitely.missing.g25");
    expect(__getMissingKeyCount()).toBe(2);
    __resetMissingKeyCount();
    expect(__getMissingKeyCount()).toBe(0);
  });

  it("does not count keys present in the current dictionary", () => {
    appState.language = "en";
    expect(translate("test.plural", { count: 1 })).toBe("1 file");
    expect(__getMissingKeyCount()).toBe(0);
  });
});
