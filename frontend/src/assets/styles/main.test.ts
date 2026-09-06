import { describe, expect, it } from "vitest";
import mainCss from "./main.css?raw";

const componentSources = import.meta.glob("../../components/**/*.vue", {
  query: "?raw",
  import: "default",
  eager: true,
}) as Record<string, string>;
const viewSources = import.meta.glob("../../views/**/*.vue", {
  query: "?raw",
  import: "default",
  eager: true,
}) as Record<string, string>;
const rootSources = import.meta.glob("../../*.vue", {
  query: "?raw",
  import: "default",
  eager: true,
}) as Record<string, string>;

function cssBlock(source: string, selector: string): string {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = source.match(new RegExp(`${escaped}\\s*\\{([^}]*)\\}`));
  if (!match) throw new Error(`missing CSS block: ${selector}`);
  return match[1];
}

function tokenValue(block: string, token: string): string {
  const escaped = token.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = block.match(new RegExp(`${escaped}\\s*:\\s*([^;]+);`));
  if (!match) throw new Error(`missing token ${token}`);
  return match[1].trim().toLowerCase();
}

function hexLuminance(value: string): number {
  const hex = value.slice(1);
  const expanded = hex.length === 3
    ? hex.split("").map((channel) => channel + channel).join("")
    : hex;
  const channels = [0, 2, 4].map((offset) =>
    Number.parseInt(expanded.slice(offset, offset + 2), 16) / 255,
  );
  const linear = channels.map((channel) =>
    channel <= 0.03928
      ? channel / 12.92
      : ((channel + 0.055) / 1.055) ** 2.4,
  );
  return 0.2126 * linear[0] + 0.7152 * linear[1] + 0.0722 * linear[2];
}

describe("light theme surface tokens", () => {
  it("defines a distinct light surface hierarchy for Apple and Claude", () => {
    const tokens = [
      "--color-bg-base",
      "--color-bg-surface",
      "--color-bg-elevated",
      "--color-bg-overlay",
      "--color-bg-surface-container",
      "--color-bg-surface-container-low",
      "--color-bg-surface-container-high",
      "--color-bg-surface-container-highest",
    ];

    for (const selector of [
      "@theme",
      '[data-mode="light"]',
      '[data-design-language="claude"][data-mode="light"]',
    ]) {
      const block = cssBlock(mainCss, selector);
      const values = tokens.map((token) => tokenValue(block, token));
      expect(new Set(values).size, selector).toBeGreaterThanOrEqual(4);
      expect(values[0], `${selector} base/elevated`).not.toBe(values[2]);
      expect(values[1], `${selector} surface/elevated`).not.toBe(values[2]);
    }
  });

  it("does not carry dark background fallbacks into light-mode failure paths", () => {
    const rendererSources = { ...rootSources, ...componentSources, ...viewSources };
    expect(Object.keys(rendererSources).length).toBeGreaterThan(0);

    const source = Object.values(rendererSources).join("\n");
    const fallbackPattern = /var\(\s*(--color-bg-[\w-]+)\s*,\s*(#[0-9a-f]{3,8})\s*\)/gi;
    const fallbacks = [...source.matchAll(fallbackPattern)].map((match) => ({
      token: match[1],
      value: match[2],
    }));
    const darkFallbacks = fallbacks
      .filter(({ value }) => hexLuminance(value) < 0.35);
    const themeBlock = cssBlock(mainCss, "@theme");
    const undefinedTokens = [...new Set(fallbacks.map(({ token }) => token))]
      .filter((token) => {
        const escaped = token.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
        return !new RegExp(`${escaped}\\s*:`).test(themeBlock);
      });

    expect(darkFallbacks).toEqual([]);
    expect(undefinedTokens).toEqual([]);
  });
});
