import { beforeEach, describe, expect, it } from "vitest";
import { appState } from "./app";
import {
  AI_SIDEBAR_MAX,
  AI_SIDEBAR_MIN,
  applyAIWindowTheme,
  normalizeAIWindowTheme,
  resolveAIWindowTheme,
  setAISidebarWidth,
  setAITerminalWidth,
} from "./aiWindow";

describe("AI window theme", () => {
  beforeEach(() => {
    document.documentElement.removeAttribute("data-ai-window-theme");
    document.documentElement.removeAttribute("data-design-language");
    document.documentElement.removeAttribute("data-mode");
    appState.theme = "light";
    appState.designLanguage = "apple";
  });

  it("normalizes supported and invalid theme values", () => {
    expect(normalizeAIWindowTheme("claude-dark")).toBe("claude-dark");
    expect(normalizeAIWindowTheme("bad-value")).toBe("apple-dark");
  });

  it("resolves system only to Apple light or dark", () => {
    expect(resolveAIWindowTheme("system", true)).toEqual({ designLanguage: "apple", mode: "light" });
    expect(resolveAIWindowTheme("system", false)).toEqual({ designLanguage: "apple", mode: "dark" });
  });

  it("resolves explicit Claude themes", () => {
    expect(resolveAIWindowTheme("claude-light", false)).toEqual({ designLanguage: "claude", mode: "light" });
  });

  it("applies theme attributes without mutating editor preferences", () => {
    applyAIWindowTheme("claude-dark", false);

    expect(document.documentElement.getAttribute("data-ai-window-theme")).toBe("claude-dark");
    expect(document.documentElement.getAttribute("data-design-language")).toBe("claude");
    expect(document.documentElement.getAttribute("data-mode")).toBe("dark");
    expect(appState.theme).toBe("light");
    expect(appState.designLanguage).toBe("apple");
  });
});

describe("AI window layout state", () => {
  it("clamps sidebar and persisted terminal widths", () => {
    expect(setAISidebarWidth(AI_SIDEBAR_MIN - 100)).toBe(AI_SIDEBAR_MIN);
    expect(setAISidebarWidth(AI_SIDEBAR_MAX + 100)).toBe(AI_SIDEBAR_MAX);
    expect(setAITerminalWidth(120)).toBe(340);
    expect(setAITerminalWidth(1200)).toBe(960);
  });
});
