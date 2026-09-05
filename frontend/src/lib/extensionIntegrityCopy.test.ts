import { describe, expect, it } from "vitest";

import en from "./locales/en";
import ja from "./locales/ja";
import zh from "./locales/zh";

describe("extension integrity copy", () => {
  it.each([
    ["en", en, /integrity|SHA-256/i],
    ["zh", zh, /哈希|完整性|SHA-256/i],
    ["ja", ja, /整合性|ハッシュ|SHA-256/i],
  ] as const)("describes the %s extension gate as a SHA-256 integrity check", (_locale, messages, integrityWording) => {
    const integrityCopy = [
      messages["extPerm.integrityUnchecked"],
      messages["extPerm.unverified"],
      messages["marketplace.securityText"],
    ];

    for (const copy of integrityCopy) {
      expect(copy).toMatch(integrityWording);
      expect(copy).not.toMatch(/signature|签名|署名/i);
    }
  });
});
