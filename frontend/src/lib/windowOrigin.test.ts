/**
 * L-10: Tests for windowOrigin — verifies the sessionStorage fallback
 * no longer leaks across instances via globalThis.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import {
  getWindowOriginId,
  unwrapEventData,
  parseSyncOrigin,
} from "./windowOrigin";

describe("windowOrigin", () => {
  describe("getWindowOriginId", () => {
    beforeEach(() => {
      // Clear sessionStorage between tests.
      try {
        sessionStorage.clear();
      } catch {
        // ignore
      }
    });

    afterEach(() => {
      vi.unstubAllGlobals();
    });

    it("returns a stable id when sessionStorage is available", () => {
      const a = getWindowOriginId();
      const b = getWindowOriginId();
      expect(a).toBe(b);
      expect(a).toMatch(/^win_/);
    });

    it("persists the id in sessionStorage", () => {
      const id = getWindowOriginId();
      expect(sessionStorage.getItem("koyori-ide.windowOriginId")).toBe(id);
    });

    it("returns different ids per call when sessionStorage is unavailable (no globalThis leak)", () => {
      // L-10: When sessionStorage throws, the fallback must NOT cache on
      // globalThis. Each call should produce an independent id.
      // Stub sessionStorage to throw on every access.
      vi.stubGlobal("sessionStorage", {
        getItem: () => {
          throw new Error("unavailable");
        },
        setItem: () => {
          throw new Error("unavailable");
        },
        removeItem: () => {
          throw new Error("unavailable");
        },
        clear: () => {
          throw new Error("unavailable");
        },
      });

      const a = getWindowOriginId();
      const b = getWindowOriginId();
      const c = getWindowOriginId();

      // Each call must produce a different id — no cross-contamination.
      expect(a).not.toBe(b);
      expect(b).not.toBe(c);
      expect(a).not.toBe(c);
      // All should still be valid ids.
      expect(a).toMatch(/^win_/);
      expect(b).toMatch(/^win_/);
      expect(c).toMatch(/^win_/);
    });

    it("does not set __gugaWindowOrigin on globalThis in the fallback path", () => {
      // Ensure no globalThis contamination from the old implementation.
      vi.stubGlobal("sessionStorage", {
        getItem: () => {
          throw new Error("unavailable");
        },
        setItem: () => {
          throw new Error("unavailable");
        },
        removeItem: () => {},
        clear: () => {},
      });

      // Clean any leftover from previous tests.
      delete (globalThis as { __gugaWindowOrigin?: string }).__gugaWindowOrigin;

      getWindowOriginId();
      getWindowOriginId();

      // The old code would have set this; the new code must not.
      expect(
        (globalThis as { __gugaWindowOrigin?: string }).__gugaWindowOrigin,
      ).toBeUndefined();
    });
  });

  describe("unwrapEventData", () => {
    it("returns null for null input", () => {
      expect(unwrapEventData(null)).toBeNull();
    });

    it("returns the raw value for non-object input", () => {
      expect(unwrapEventData(42)).toBe(42);
      expect(unwrapEventData("hello")).toBe("hello");
    });

    it("unwraps { data } objects", () => {
      expect(unwrapEventData({ data: "payload" })).toBe("payload");
    });

    it("unwraps array-wrapped data", () => {
      expect(unwrapEventData({ data: ["arr-payload"] })).toBe("arr-payload");
    });

    it("returns the first element for arrays", () => {
      expect(unwrapEventData(["first", "second"])).toBe("first");
    });
  });

  describe("parseSyncOrigin", () => {
    it("extracts origin string from payload", () => {
      expect(parseSyncOrigin({ origin: "win_abc123" })).toBe("win_abc123");
    });

    it("returns empty string for missing origin", () => {
      expect(parseSyncOrigin({})).toBe("");
      expect(parseSyncOrigin(null)).toBe("");
    });

    it("returns empty string for non-string origin", () => {
      expect(parseSyncOrigin({ origin: 42 })).toBe("");
    });
  });
});
