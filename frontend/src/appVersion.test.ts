/* global __APP_VERSION__ */

import { describe, expect, it } from "vitest";

// P9-G08 AC2: __APP_VERSION__ is injected by vite.config.ts / vitest.config.ts
// from the single VERSION file, so Welcome/About shows the packaged version.
describe("app version SSOT", () => {
  it("injects a SemVer renderer constant", () => {
    expect(typeof __APP_VERSION__).toBe("string");
    expect(__APP_VERSION__).toMatch(/^[0-9]+\.[0-9]+\.[0-9]+/);
  });

  it("exposes a non-empty renderer constant", () => {
    expect(__APP_VERSION__.length).toBeGreaterThan(0);
  });
});