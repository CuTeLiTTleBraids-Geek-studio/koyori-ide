import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/monaco-themes", () => ({
  accentThemes: {},
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
  __resetLocaleDictionary,
  __setLocaleDictionary,
  registerLocaleMessages,
  SECURITY_MESSAGE_CATALOG,
  translate,
} from "./i18n";
import {
  isSecurityMessageKey,
  SECURITY_MESSAGE_KEYS,
} from "./languagePackRuntime";

describe("language pack security message boundary", () => {
  beforeEach(() => {
    appState.language = "en";
    __resetLocaleDictionary("en");
  });

  it("rejects security key overrides atomically", () => {
    expect(() =>
      registerLocaleMessages("en", {
        "extension.normalLabel": "Allowed label",
        "aiChat.riskDangerous": "Totally safe",
      }),
    ).toThrow(/security key/i);

    expect(translate("extension.normalLabel")).toBe("extension.normalLabel");
  });

  it("accepts ordinary external messages", () => {
    registerLocaleMessages("en", {
      "extension.normalLabel": "External label",
    });

    expect(translate("extension.normalLabel")).toBe("External label");
  });

  it.each([
    "AICHAT.RISKSAFE",
    "ａｉＣｈａｔ．ｒｉｓｋＳａｆｅ",
  ])("rejects normalized security-key collision %s", (key) => {
    expect(isSecurityMessageKey(key)).toBe(true);
    expect(() => registerLocaleMessages("en", { [key]: "spoofed" })).toThrow(
      /security key/i,
    );
  });

  it("keeps security translations pinned to the immutable built-in catalog", () => {
    const key = "marketplace.securityTitle";
    const builtInValue = SECURITY_MESSAGE_CATALOG.en[key];

    __setLocaleDictionary("en", { [key]: "External value" });

    expect(Object.isFrozen(SECURITY_MESSAGE_KEYS)).toBe(true);
    expect(Object.isFrozen(SECURITY_MESSAGE_CATALOG.en)).toBe(true);
    expect(translate(key)).toBe(builtInValue);
    expect(translate(key)).not.toBe("External value");
  });
});
