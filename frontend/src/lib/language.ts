// Koyori IDE 模块 · Language。
// 喵，这是 Koyori IDE 的 Language 模块（前端实现）~
import {
  createBuiltInLanguagePackRegistry,
  type LanguagePackRuntimeContribution,
} from "./languagePackRuntime";

const extensionToLanguage: Record<string, string> = {
  vue: "html",
  html: "html",
  htm: "html",
  css: "css",
  scss: "scss",
  sass: "sass",
  less: "less",
  gohtml: "go-template",
  tmpl: "go-template",
  gotmpl: "go-template",
  py: "python",
  rs: "rust",
  java: "java",
  c: "c",
  cpp: "cpp",
  cs: "csharp",
  rb: "ruby",
  php: "php",
  swift: "swift",
  kt: "kotlin",
  json: "json",
  jsonc: "json",
  xml: "xml",
  yaml: "yaml",
  yml: "yaml",
  toml: "ini",
  ini: "ini",
  md: "markdown",
  markdown: "markdown",
  sh: "shell",
  bash: "shell",
  zsh: "shell",
  sql: "sql",
  dockerfile: "dockerfile",
};

let activeLanguagePacks = createBuiltInLanguagePackRegistry();

export function replaceExternalLanguagePackContributions(
  contributions: readonly LanguagePackRuntimeContribution[],
): void {
  const next = createBuiltInLanguagePackRegistry();
  for (const contribution of contributions)
    next.registerRuntimeContribution(contribution);
  activeLanguagePacks = next;
}

export function detectLanguage(filePath: string): string {
  const packedLanguage = activeLanguagePacks.detect(filePath);
  if (packedLanguage) return packedLanguage;
  const fileName = filePath.split(/[/\\]/).pop() ?? filePath;
  const lowerName = fileName.toLowerCase();
  if (lowerName === "dockerfile") return "dockerfile";
  const ext = lowerName.split(".").pop() ?? "";
  return extensionToLanguage[ext] ?? "plaintext";
}
