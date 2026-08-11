// Koyori IDE 模块 · Ai Window。
// 喵，这是 Koyori IDE 的 Ai Window 模块（前端实现）~
import { reactive } from "vue";
import type { AIWindowTheme } from "@/types";

export type { AIWindowTheme } from "@/types";

export const AI_SIDEBAR_MIN = 260;
export const AI_SIDEBAR_MAX = 380;
export const AI_TERMINAL_MIN = 340;
export const AI_TERMINAL_MAX_PERSISTED = 960;

export type AIWorkspaceView =
  | "assistant"
  | "skills"
  | "automation"
  | "settings"
  | "rollback";

export interface AIWindowState {
  activeView: AIWorkspaceView;
  sidebarWidth: number;
  terminalVisible: boolean;
  terminalWidth: number;
  theme: AIWindowTheme;
}

export const aiWindowState = reactive<AIWindowState>({
  activeView: "assistant",
  sidebarWidth: 288,
  terminalVisible: false,
  terminalWidth: 440,
  theme: "apple-dark",
});

export function normalizeAIWindowTheme(value: unknown): AIWindowTheme {
  switch (value) {
    case "apple-dark":
    case "apple-light":
    case "claude-dark":
    case "claude-light":
    case "system":
      return value;
    default:
      return "apple-dark";
  }
}

export function resolveAIWindowTheme(
  theme: AIWindowTheme,
  prefersLight: boolean,
): { designLanguage: "apple" | "claude"; mode: "dark" | "light" } {
  if (theme === "system") {
    return { designLanguage: "apple", mode: prefersLight ? "light" : "dark" };
  }
  const [designLanguage, mode] = theme.split("-") as [
    "apple" | "claude",
    "dark" | "light",
  ];
  return { designLanguage, mode };
}

export function applyAIWindowTheme(
  theme: AIWindowTheme,
  prefersLight = window.matchMedia?.("(prefers-color-scheme: light)").matches ?? false,
): void {
  const normalized = normalizeAIWindowTheme(theme);
  const resolved = resolveAIWindowTheme(normalized, prefersLight);
  const root = document.documentElement;
  root.setAttribute("data-ai-window-theme", normalized);
  root.setAttribute("data-mode", resolved.mode);
  if (resolved.designLanguage === "claude") {
    root.setAttribute("data-design-language", "claude");
  } else {
    root.removeAttribute("data-design-language");
  }
}

export function setAISidebarWidth(width: number): number {
  const next = clamp(width, AI_SIDEBAR_MIN, AI_SIDEBAR_MAX);
  aiWindowState.sidebarWidth = next;
  return next;
}

export function setAITerminalWidth(width: number): number {
  const next = clamp(width, AI_TERMINAL_MIN, AI_TERMINAL_MAX_PERSISTED);
  aiWindowState.terminalWidth = next;
  return next;
}

export function syncAIWindowPreferences(input: {
  theme: unknown;
  sidebarWidth: number;
  terminalWidth: number;
}): void {
  aiWindowState.theme = normalizeAIWindowTheme(input.theme);
  setAISidebarWidth(input.sidebarWidth);
  setAITerminalWidth(input.terminalWidth);
}

export function getTerminalMaxWidth(contentWidth: number): number {
  return Math.max(AI_TERMINAL_MIN, Math.floor(contentWidth * 0.55));
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}
