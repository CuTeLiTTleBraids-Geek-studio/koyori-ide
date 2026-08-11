import { beforeEach, describe, expect, it, vi } from "vitest";

const { defineTheme, setTheme } = vi.hoisted(() => ({
  defineTheme: vi.fn(),
  setTheme: vi.fn(),
}));

vi.mock("monaco-editor", () => ({
  editor: { defineTheme, setTheme },
}));

import {
  getMonacoThemeNameForMode,
  registerAllThemes,
} from "./monaco-themes";

describe("Monaco theme registry", () => {
  beforeEach(() => vi.clearAllMocks());

  it("registers six bracket colors and semantic token rules in both modes", () => {
    registerAllThemes();
    const darkTheme = defineTheme.mock.calls.find(([name]) => name === "koyoriIde-blue")?.[1];
    const lightTheme = defineTheme.mock.calls.find(([name]) => name === "koyoriIde-light-blue")?.[1];

    for (const theme of [darkTheme, lightTheme]) {
      expect(theme).toBeDefined();
      for (let index = 1; index <= 6; index += 1) {
        expect(theme.colors[`editorBracketHighlight.foreground${index}`]).toBeTruthy();
      }
      expect(theme.rules).toEqual(expect.arrayContaining([
        expect.objectContaining({ token: "function.declaration" }),
        expect.objectContaining({ token: "variable.readonly" }),
        expect.objectContaining({ token: "interface" }),
      ]));
    }
  });

  it("uses Monaco high-contrast themes for system contrast preferences", () => {
    expect(getMonacoThemeNameForMode("blue", "dark", true)).toBe("hc-black");
    expect(getMonacoThemeNameForMode("blue", "light", true)).toBe("hc-light");
  });
});
