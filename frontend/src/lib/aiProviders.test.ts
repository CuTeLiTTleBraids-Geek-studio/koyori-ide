import { describe, it, expect } from "vitest";
import { PROVIDER_PRESETS, getProviderPreset, getSuggestedModels } from "./aiProviders";

describe("aiProviders", () => {
  it("PROVIDER_PRESETS includes the standard providers", () => {
    const ids = PROVIDER_PRESETS.map((p) => p.id);
    expect(ids).toContain("openai");
    expect(ids).toContain("azure");
    expect(ids).toContain("anthropic");
    expect(ids).toContain("gemini");
    expect(ids).toContain("ollama");
    expect(ids).toContain("lmstudio");
    expect(ids).toContain("custom");
  });

  it("each preset has a unique id", () => {
    const ids = PROVIDER_PRESETS.map((p) => p.id);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it("OpenAI preset has a non-empty base URL and models", () => {
    const openai = getProviderPreset("openai");
    expect(openai).toBeDefined();
    expect(openai!.baseUrl).not.toBe("");
    expect(openai!.models.length).toBeGreaterThan(0);
  });

  it("local providers are flagged", () => {
    expect(getProviderPreset("ollama")?.local).toBe(true);
    expect(getProviderPreset("lmstudio")?.local).toBe(true);
    expect(getProviderPreset("openai")?.local).toBeUndefined();
  });

  it("getProviderPreset returns undefined for unknown id", () => {
    expect(getProviderPreset("nonexistent")).toBeUndefined();
  });

  it("getSuggestedModels returns the preset's models", () => {
    const models = getSuggestedModels("openai");
    expect(models).toContain("gpt-4o");
    expect(models.length).toBeGreaterThan(0);
  });

  it("getSuggestedModels returns empty array for unknown provider", () => {
    expect(getSuggestedModels("nonexistent")).toEqual([]);
  });

  it("getSuggestedModels returns empty array for Custom (no presets)", () => {
    expect(getSuggestedModels("custom")).toEqual([]);
  });

  it("presets that need OpenAI-compat paths do not end with bare /v1", () => {
    for (const preset of PROVIDER_PRESETS) {
      if (!preset.baseUrl) continue;
      expect(preset.baseUrl.replace(/\/+$/, "").toLowerCase().endsWith("/v1")).toBe(false);
    }
  });

  it("Gemini preset uses the OpenAI-compatible base path", () => {
    expect(getProviderPreset("gemini")?.baseUrl).toBe(
      "https://generativelanguage.googleapis.com/v1beta/openai",
    );
  });
});
