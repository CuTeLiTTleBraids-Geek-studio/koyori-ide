import { describe, it, expect } from "vitest";

// Plan 57 / N-23: themeEditor.ts is a pure module with no side-effect imports
// (only imports types), so no mocks are needed. The previous mocks for
// Wails runtime, monaco-editor, settingsService, rules store, and
// useKeyboard were only necessary when the test imported from ./app.

import {
  deriveAccentTokens,
  serializeCustomAccent,
  deserializeCustomAccent,
} from "./themeEditor";
import type { CustomAccentTheme } from "@/types";

describe("Plan 48 Theme Editor", () => {
  describe("deriveAccentTokens", () => {
    it("derives all 8 tokens from a base color", () => {
      const custom: CustomAccentTheme = {
        name: "Test",
        color: "#ff6b35",
      };
      const tokens = deriveAccentTokens(custom);
      expect(tokens["--color-primary"]).toBe("#ff6b35");
      expect(tokens["--color-primary-hover"]).toBe("#ff6b35ee");
      expect(tokens["--color-primary-light"]).toBe("#ff6b3530");
      expect(tokens["--color-primary-container"]).toBe("#ff6b3520");
      expect(tokens["--color-on-primary"]).toBe("#ffffff");
      expect(tokens["--color-on-primary-container"]).toBe("#ff6b35");
      expect(tokens["--color-tertiary"]).toBe("#ff6b3530");
      expect(tokens["--el-color-primary"]).toBe("#ff6b35");
    });

    it("uses overrides when provided", () => {
      const custom: CustomAccentTheme = {
        name: "Override",
        color: "#ff6b35",
        primary: "#custom1",
        primaryHover: "#custom2",
        primaryLight: "#custom3",
        primaryContainer: "#custom4",
        onPrimary: "#custom5",
        onPrimaryContainer: "#custom6",
      };
      const tokens = deriveAccentTokens(custom);
      expect(tokens["--color-primary"]).toBe("#custom1");
      expect(tokens["--color-primary-hover"]).toBe("#custom2");
      expect(tokens["--color-primary-light"]).toBe("#custom3");
      expect(tokens["--color-primary-container"]).toBe("#custom4");
      expect(tokens["--color-on-primary"]).toBe("#custom5");
      expect(tokens["--color-on-primary-container"]).toBe("#custom6");
      expect(tokens["--color-tertiary"]).toBe("#custom3");
      expect(tokens["--el-color-primary"]).toBe("#custom1");
    });

    it("tertiary mirrors primaryLight override", () => {
      const custom: CustomAccentTheme = {
        name: "T",
        color: "#abcdef",
        primaryLight: "#override",
      };
      const tokens = deriveAccentTokens(custom);
      expect(tokens["--color-tertiary"]).toBe("#override");
    });

    it("el-color-primary mirrors primary override", () => {
      const custom: CustomAccentTheme = {
        name: "T",
        color: "#abcdef",
        primary: "#elpri",
      };
      const tokens = deriveAccentTokens(custom);
      expect(tokens["--el-color-primary"]).toBe("#elpri");
    });

    it("handles short hex colors", () => {
      const custom: CustomAccentTheme = {
        name: "Short",
        color: "#abc",
      };
      const tokens = deriveAccentTokens(custom);
      expect(tokens["--color-primary"]).toBe("#abc");
      expect(tokens["--color-primary-hover"]).toBe("#abcee");
    });
  });

  describe("serializeCustomAccent", () => {
    it("produces valid JSON with all fields", () => {
      const custom: CustomAccentTheme = {
        name: "Sunset",
        color: "#ff6b35",
        primary: "#ff6b35",
      };
      const json = serializeCustomAccent(custom);
      const parsed = JSON.parse(json);
      expect(parsed.name).toBe("Sunset");
      expect(parsed.color).toBe("#ff6b35");
      expect(parsed.primary).toBe("#ff6b35");
    });

    it("omits undefined optional fields", () => {
      const custom: CustomAccentTheme = {
        name: "Minimal",
        color: "#abcdef",
      };
      const json = serializeCustomAccent(custom);
      const parsed = JSON.parse(json);
      expect(parsed.name).toBe("Minimal");
      expect(parsed.color).toBe("#abcdef");
      expect(parsed.primary).toBeUndefined();
      expect(parsed.primaryHover).toBeUndefined();
    });

    it("produces pretty-printed JSON", () => {
      const custom: CustomAccentTheme = {
        name: "Pretty",
        color: "#123456",
      };
      const json = serializeCustomAccent(custom);
      expect(json).toContain("\n");
      expect(json).toContain('  "name"');
    });
  });

  describe("deserializeCustomAccent", () => {
    it("parses valid JSON with required fields", () => {
      const json = JSON.stringify({
        name: "Ocean",
        color: "#26c6da",
      });
      const custom = deserializeCustomAccent(json);
      expect(custom.name).toBe("Ocean");
      expect(custom.color).toBe("#26c6da");
      expect(custom.primary).toBeUndefined();
    });

    it("parses JSON with optional overrides", () => {
      const json = JSON.stringify({
        name: "Full",
        color: "#ff6b35",
        primary: "#p",
        primaryHover: "#ph",
        primaryLight: "#pl",
        primaryContainer: "#pc",
        onPrimary: "#op",
        onPrimaryContainer: "#opc",
      });
      const custom = deserializeCustomAccent(json);
      expect(custom.primary).toBe("#p");
      expect(custom.primaryHover).toBe("#ph");
      expect(custom.primaryLight).toBe("#pl");
      expect(custom.primaryContainer).toBe("#pc");
      expect(custom.onPrimary).toBe("#op");
      expect(custom.onPrimaryContainer).toBe("#opc");
    });

    it("throws when name is missing", () => {
      const json = JSON.stringify({ color: "#ff6b35" });
      expect(() => deserializeCustomAccent(json)).toThrow(/name and color are required/);
    });

    it("throws when color is missing", () => {
      const json = JSON.stringify({ name: "NoColor" });
      expect(() => deserializeCustomAccent(json)).toThrow(/name and color are required/);
    });

    it("throws when color is not a valid hex", () => {
      const json = JSON.stringify({ name: "Bad", color: "red" });
      expect(() => deserializeCustomAccent(json)).toThrow(/Invalid theme color/);
    });

    it("throws when color is a 3-digit hex (requires 6)", () => {
      const json = JSON.stringify({ name: "Short", color: "#abc" });
      expect(() => deserializeCustomAccent(json)).toThrow(/Invalid theme color/);
    });

    it("throws on invalid JSON", () => {
      expect(() => deserializeCustomAccent("not json")).toThrow();
    });

    it("ignores unknown fields in the JSON", () => {
      const json = JSON.stringify({
        name: "Extra",
        color: "#ff6b35",
        unknownField: "ignored",
        another: 42,
      });
      const custom = deserializeCustomAccent(json);
      expect(custom.name).toBe("Extra");
      expect(custom.color).toBe("#ff6b35");
    });

    it("treats non-string optional fields as undefined", () => {
      const json = JSON.stringify({
        name: "Mixed",
        color: "#ff6b35",
        primary: 123,
        primaryHover: null,
      });
      const custom = deserializeCustomAccent(json);
      expect(custom.primary).toBeUndefined();
      expect(custom.primaryHover).toBeUndefined();
    });
  });

  describe("serialize → deserialize round trip", () => {
    it("round-trips a minimal theme", () => {
      const original: CustomAccentTheme = {
        name: "Round",
        color: "#a1b2c3",
      };
      const json = serializeCustomAccent(original);
      const restored = deserializeCustomAccent(json);
      expect(restored.name).toBe(original.name);
      expect(restored.color).toBe(original.color);
      expect(restored.primary).toBeUndefined();
    });

    it("round-trips a full theme with overrides", () => {
      const original: CustomAccentTheme = {
        name: "Full",
        color: "#ff6b35",
        primary: "#p1",
        primaryHover: "#p2",
        primaryLight: "#p3",
        primaryContainer: "#p4",
        onPrimary: "#p5",
        onPrimaryContainer: "#p6",
      };
      const json = serializeCustomAccent(original);
      const restored = deserializeCustomAccent(json);
      expect(restored).toEqual(original);
    });
  });
});
