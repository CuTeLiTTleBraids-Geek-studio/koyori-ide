import { describe, it, expect } from "vitest";
import { detectLanguage } from "./language";

describe("detectLanguage", () => {
  it("detects TypeScript", () => {
    expect(detectLanguage("foo.ts")).toBe("typescript");
    expect(detectLanguage("foo.tsx")).toBe("typescriptreact");
  });

  it("detects JavaScript", () => {
    expect(detectLanguage("foo.js")).toBe("javascript");
    expect(detectLanguage("foo.jsx")).toBe("javascriptreact");
  });

  it("detects Vue", () => {
    expect(detectLanguage("App.vue")).toBe("html");
  });

  it("detects Go", () => {
    expect(detectLanguage("main.go")).toBe("go");
  });

  it.each(["page.gohtml", "layout.tmpl", "partial.gotmpl"])(
    "detects Go template files (%s)",
    (filePath) => {
      expect(detectLanguage(filePath)).toBe("go-template");
    },
  );

  it("detects JSON", () => {
    expect(detectLanguage("package.json")).toBe("json");
    expect(detectLanguage("settings.jsonc")).toBe("json");
  });

  it("detects CSS", () => {
    expect(detectLanguage("style.css")).toBe("css");
  });

  it("detects Markdown", () => {
    expect(detectLanguage("README.md")).toBe("markdown");
  });

  it("returns plaintext for unknown extensions", () => {
    expect(detectLanguage("file.xyz")).toBe("plaintext");
  });

  it("returns plaintext for files with no extension", () => {
    expect(detectLanguage("Makefile")).toBe("plaintext");
  });

  it("handles paths with directories", () => {
    expect(detectLanguage("src/components/App.vue")).toBe("html");
  });
});
