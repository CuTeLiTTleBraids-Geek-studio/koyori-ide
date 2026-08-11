/**
 * NK NK Coding — Monaco Theme Registry
 * Each accent color maps to a coordinated Monaco editor theme
 * ensuring visual harmony between UI chrome and code surface.
 */
// Koyori IDE 模块 · Monaco Themes。
// 喵，这是 Koyori IDE 的 Monaco Themes 模块（前端实现）~

import * as monaco from "monaco-editor";

export type AccentTheme = "blue" | "teal" | "green" | "amber" | "pink" | "purple" | "cyan" | "indigo" | "custom";

export interface ThemeMeta {
  label: string;
  color: string;
  monacoTheme: string;
  monacoLightTheme: string;
}

export const accentThemes: Record<AccentTheme, ThemeMeta> = {
  blue:   { label: "Blue",   color: "#4285f4", monacoTheme: "koyoriIde-blue",   monacoLightTheme: "koyoriIde-light-blue" },
  teal:   { label: "Teal",   color: "#26a69a", monacoTheme: "koyoriIde-teal",   monacoLightTheme: "koyoriIde-light-teal" },
  green:  { label: "Green",  color: "#66bb6a", monacoTheme: "koyoriIde-green",  monacoLightTheme: "koyoriIde-light-green" },
  amber:  { label: "Amber",  color: "#ffa726", monacoTheme: "koyoriIde-amber",  monacoLightTheme: "koyoriIde-light-amber" },
  pink:   { label: "Pink",   color: "#ec407a", monacoTheme: "koyoriIde-pink",   monacoLightTheme: "koyoriIde-light-pink" },
  purple: { label: "Purple", color: "#ab47bc", monacoTheme: "koyoriIde-purple", monacoLightTheme: "koyoriIde-light-purple" },
  cyan:   { label: "Cyan",   color: "#26c6da", monacoTheme: "koyoriIde-cyan",   monacoLightTheme: "koyoriIde-light-cyan" },
  indigo: { label: "Indigo", color: "#5c6bc0", monacoTheme: "koyoriIde-indigo", monacoLightTheme: "koyoriIde-light-indigo" },
  // Plan 48: custom accent. The color is a placeholder — the actual Monaco
  // theme is registered dynamically via registerCustomTheme() before apply.
  custom: { label: "Custom", color: "#ff6b35", monacoTheme: "koyoriIde-custom", monacoLightTheme: "koyoriIde-light-custom" },
};

function createThemeData(accent: string): monaco.editor.IStandaloneThemeData {
  return {
    base: "vs-dark",
    inherit: true,
    semanticHighlighting: true,
    rules: [
      { token: "comment",             foreground: "747678", fontStyle: "italic" },
      { token: "keyword",            foreground: accent },
      { token: "keyword.control",    foreground: accent },
      { token: "string",             foreground: "#a8dab5" },
      { token: "string.escape",      foreground: "#ce93d8", fontStyle: "bold" },
      { token: "number",             foreground: "#ffa726" },
      { token: "regexp",             foreground: "#80deea" },
      { token: "type",               foreground: "#80cbc4" },
      { token: "class",              foreground: "#ffcc80" },
      { token: "function",           foreground: "#a8c7fa" },
      { token: "variable",          foreground: "#e3e2e6" },
      { token: "variable.predefined",foreground: "#f48fb1" },
      { token: "constant",           foreground: "#ce93d8" },
      { token: "tag",                foreground: "#f48fb1" },
      { token: "attribute.name",      foreground: "#a8c7fa" },
      { token: "attribute.value",     foreground: "#a8dab5" },
      { token: "delimiter",           foreground: "a1a2a7" },
      { token: "delimiter.bracket",  foreground: "c4c6c9" },
      { token: "operator",           foreground: accent },
      { token: "meta",               foreground: "a1a2a7" },
      { token: "meta.tag",           foreground: "c4c6c9" },
      { token: "namespace",          foreground: "80cbc4" },
      { token: "enum",               foreground: "ffcc80" },
      { token: "interface",          foreground: "80cbc4" },
      { token: "struct",             foreground: "ffcc80" },
      { token: "typeParameter",      foreground: "ce93d8" },
      { token: "parameter",          foreground: "e3e2e6" },
      { token: "variable.readonly",  foreground: "ce93d8" },
      { token: "property",           foreground: "9cdcfe" },
      { token: "enumMember",         foreground: "ce93d8" },
      { token: "function.declaration", foreground: "a8c7fa", fontStyle: "bold" },
      { token: "method.declaration", foreground: "a8c7fa", fontStyle: "bold" },
    ],
    colors: {
      "editor.background":             "#111114",
      "editor.foreground":             "#e3e2e6",
      "editor.lineHighlightBackground": "#1b1b1f",
      "editor.selectionBackground":     accent + "30",
      "editor.inactiveSelectionBackground": accent + "18",
      "editorLineNumber.foreground":    "#525355",
      "editorLineNumber.activeForeground": "#a1a2a7",
      "editorCursor.foreground":       "#e3e2e6",
      "editorWhitespace.foreground":   "#333338",
      "editorIndentGuide.background":  "#1e1e23",
      "editorIndentGuide.activeBackground": "#2e2e35",
      "editorBracketMatch.background": accent + "25",
      "editorBracketMatch.border":     accent + "60",
      "editorBracketHighlight.foreground1": "#ffd700",
      "editorBracketHighlight.foreground2": "#da70d6",
      "editorBracketHighlight.foreground3": "#87cefa",
      "editorBracketHighlight.foreground4": "#ff8c69",
      "editorBracketHighlight.foreground5": "#98fb98",
      "editorBracketHighlight.foreground6": "#dda0dd",
      "editorBracketHighlight.unexpectedBracket.foreground": "#ff5c5c",
      "editorGutter.background":       "#111114",
      "editorWidget.background":       "#1b1b1f",
      "editorWidget.border":           "#2e2e35",
      "editorSuggestWidget.background": "#1b1b1f",
      "editorSuggestWidget.border":     "#2e2e35",
      "editorSuggestWidget.selectedBackground": accent + "20",
      "editorHoverWidget.background":  "#1b1b1f",
      "editorHoverWidget.border":      "#2e2e35",
      "peekViewEditor.background":     "#16161a",
      "peekViewResult.background":      "#16161a",
      "peekViewTitle.background":       "#1b1b1f",
      "minimap.background":             "#111114",
      "scrollbarSlider.background":     "#33333860",
      "scrollbarSlider.hoverBackground": "#44445090",
      "scrollbarSlider.activeBackground": accent + "80",
      "input.background":               "#1e1e23",
      "input.border":                   "#2e2e35",
      "inputOption.activeBackground":   accent + "20",
      "focusBorder":                     accent + "60",
      "list.activeSelectionBackground": accent + "18",
      "list.hoverBackground":           "#1e1e23",
      "list.highlightForeground":       accent,
      "findMatchBackground":            accent + "35",
      "findMatchHighlightBackground":    accent + "25",
      "findRangeHighlightBackground":    accent + "10",
    },
  } as monaco.editor.IStandaloneThemeData;
}

function createLightThemeData(accent: string): monaco.editor.IStandaloneThemeData {
  return {
    base: "vs",
    inherit: true,
    semanticHighlighting: true,
    rules: [
      { token: "comment",             foreground: "747678", fontStyle: "italic" },
      { token: "keyword",            foreground: accent },
      { token: "keyword.control",    foreground: accent },
      { token: "string",             foreground: "1f6b3a" },
      { token: "string.escape",      foreground: "8e24aa", fontStyle: "bold" },
      { token: "number",             foreground: "b25f00" },
      { token: "regexp",             foreground: "006777" },
      { token: "type",               foreground: "006a6a" },
      { token: "class",              foreground: "9a4a00" },
      { token: "function",           foreground: "1858b4" },
      { token: "variable",          foreground: "1b1b1f" },
      { token: "variable.predefined",foreground: "b0146f" },
      { token: "constant",           foreground: "8e24aa" },
      { token: "tag",                foreground: "b0146f" },
      { token: "attribute.name",      foreground: "1858b4" },
      { token: "attribute.value",     foreground: "1f6b3a" },
      { token: "delimiter",           foreground: "44474e" },
      { token: "delimiter.bracket",  foreground: "44474e" },
      { token: "operator",           foreground: accent },
      { token: "meta",               foreground: "44474e" },
      { token: "meta.tag",           foreground: "44474e" },
      { token: "namespace",          foreground: "006a6a" },
      { token: "enum",               foreground: "9a4a00" },
      { token: "interface",          foreground: "006a6a" },
      { token: "struct",             foreground: "9a4a00" },
      { token: "typeParameter",      foreground: "8e24aa" },
      { token: "parameter",          foreground: "1b1b1f" },
      { token: "variable.readonly",  foreground: "8e24aa" },
      { token: "property",           foreground: "1858b4" },
      { token: "enumMember",         foreground: "8e24aa" },
      { token: "function.declaration", foreground: "1858b4", fontStyle: "bold" },
      { token: "method.declaration", foreground: "1858b4", fontStyle: "bold" },
    ],
    colors: {
      "editor.background":             "#fefcff",
      "editor.foreground":             "#1b1b1f",
      "editor.lineHighlightBackground": "#f4f3f8",
      "editor.selectionBackground":     accent + "30",
      "editor.inactiveSelectionBackground": accent + "18",
      "editorLineNumber.foreground":    "#c4c6c9",
      "editorLineNumber.activeForeground": "#44474e",
      "editorCursor.foreground":       "#1b1b1f",
      "editorWhitespace.foreground":   "#dbd9de",
      "editorIndentGuide.background":  "#eeeef3",
      "editorIndentGuide.activeBackground": "#c4c6c9",
      "editorBracketMatch.background": accent + "25",
      "editorBracketMatch.border":     accent + "60",
      "editorBracketHighlight.foreground1": "#0431fa",
      "editorBracketHighlight.foreground2": "#7a3e9d",
      "editorBracketHighlight.foreground3": "#007c91",
      "editorBracketHighlight.foreground4": "#b45f06",
      "editorBracketHighlight.foreground5": "#2e7d32",
      "editorBracketHighlight.foreground6": "#a23e48",
      "editorBracketHighlight.unexpectedBracket.foreground": "#b42318",
      "editorGutter.background":       "#fefcff",
      "editorWidget.background":       "#ffffff",
      "editorWidget.border":           "#e3e2e7",
      "editorSuggestWidget.background": "#ffffff",
      "editorSuggestWidget.border":     "#e3e2e7",
      "editorSuggestWidget.selectedBackground": accent + "20",
      "editorHoverWidget.background":  "#ffffff",
      "editorHoverWidget.border":      "#e3e2e7",
      "peekViewEditor.background":     "#f4f3f8",
      "peekViewResult.background":      "#f4f3f8",
      "peekViewTitle.background":       "#ffffff",
      "minimap.background":             "#fefcff",
      "scrollbarSlider.background":     "#c4c6c960",
      "scrollbarSlider.hoverBackground": "#74767890",
      "scrollbarSlider.activeBackground": accent + "80",
      "input.background":               "#f4f3f8",
      "input.border":                   "#e3e2e7",
      "inputOption.activeBackground":   accent + "20",
      "focusBorder":                     accent + "60",
      "list.activeSelectionBackground": accent + "18",
      "list.hoverBackground":           "#f4f3f8",
      "list.highlightForeground":       accent,
      "findMatchBackground":            accent + "35",
      "findMatchHighlightBackground":    accent + "25",
      "findRangeHighlightBackground":    accent + "10",
    },
  } as monaco.editor.IStandaloneThemeData;
}

/**
 * Register all Monaco themes.
 * Call once at app init (e.g. in main.ts).
 */
export function registerAllThemes(): void {
  for (const [key, meta] of Object.entries(accentThemes)) {
    if (key === "custom") continue; // custom is registered dynamically
    const darkData = createThemeData(meta.color);
    monaco.editor.defineTheme(meta.monacoTheme, darkData);
    const lightData = createLightThemeData(meta.color);
    monaco.editor.defineTheme(meta.monacoLightTheme, lightData);
  }
}

/**
 * Register (or re-register) the custom Monaco theme using the given accent
 * color (Plan 48). Must be called before applyMonacoTheme("custom") or
 * applyMonacoThemeForMode("custom", mode). Safe to call multiple times —
 * defineTheme overwrites the previous definition.
 */
export function registerCustomTheme(color: string): void {
  monaco.editor.defineTheme("koyoriIde-custom", createThemeData(color));
  monaco.editor.defineTheme("koyoriIde-light-custom", createLightThemeData(color));
}

/**
 * Set Monaco editor theme to match current accent.
 */
export function applyMonacoTheme(accent: AccentTheme): void {
  const theme = accentThemes[accent];
  if (theme) {
    monaco.editor.setTheme(theme.monacoTheme);
  }
}

/**
 * Set Monaco editor theme to match current accent and mode.
 */
export function applyMonacoThemeForMode(accent: AccentTheme, mode: "dark" | "light"): void {
  const theme = accentThemes[accent];
  if (theme) {
    const themeName = mode === "light" ? theme.monacoLightTheme : theme.monacoTheme;
    monaco.editor.setTheme(themeName);
  }
}

/**
 * Get the Monaco theme name for an accent and mode.
 */
export function getMonacoThemeNameForMode(
  accent: AccentTheme,
  mode: "dark" | "light",
  highContrast = false,
): string {
  if (highContrast) return mode === "light" ? "hc-light" : "hc-black";
  const theme = accentThemes[accent];
  if (!theme) return "koyoriIde-blue";
  return mode === "light" ? theme.monacoLightTheme : theme.monacoTheme;
}

/**
 * Get the Monaco theme name for an accent.
 */
export function getMonacoThemeName(accent: AccentTheme): string {
  return accentThemes[accent]?.monacoTheme ?? "koyoriIde-blue";
}
