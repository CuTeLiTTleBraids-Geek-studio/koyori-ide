/**
 * G-COMP-03: Auto-import module.
 *
 * When the user types an identifier that matches an exported symbol in another
 * workspace file, this module provides a completion suggestion whose
 * additionalTextEdits insert the appropriate import statement at the top of the
 * file.
 *
 * Supports three module systems:
 *   - ES modules (import { X } from './path' / import X from './path')
 *   - CommonJS  (const { X } = require('./path') / const X = require('./path'))
 *   - Go        (adds "pkg/path" to the import block)
 *
 * This is a fallback for when the LSP server does not provide auto-import
 * (e.g. server not running, or file outside LSP workspace). LSP-provided
 * additionalTextEdits take priority.
 */
// Koyori IDE 模块 · Auto Import；交互服务：符号索引（SymbolIndexService）。
// 喵，这是 Koyori IDE 的 Auto Import 模块（前端实现）~

import type * as monacoEditor from "monaco-editor";
import type { IndexedSymbol } from "@/types";
import { symbolIndexService } from "@/api/services";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** A Monaco completion item pre-filled with auto-import additionalTextEdits. */
export interface AutoImportSuggestion {
  label: string;
  kind: number;
  detail: string;
  insertText: string;
  sortText: string;
  documentation?: string;
  additionalTextEdits: Array<{
    range: {
      startLineNumber: number;
      startColumn: number;
      endLineNumber: number;
      endColumn: number;
    };
    text: string;
  }>;
}

type ModuleSystem = "esm" | "cjs" | "go";

// ---------------------------------------------------------------------------
// Language group classification (cross-language isolation)
// ---------------------------------------------------------------------------

/**
 * 将文件路径映射到语言分组。auto-import 只在同语言分组内匹配符号，
 * 避免后端语言（Go）看到前端语言（JS/TS）的变量，反之亦然。
 * 对标 VSCode：每个语言服务器的 auto-import 严格按语言隔离。
 */
type LanguageGroup = "go" | "js-family" | "other";
function languageGroupOf(filePath: string): LanguageGroup {
  const lower = filePath.toLowerCase();
  if (lower.endsWith(".go")) return "go";
  if (
    lower.endsWith(".ts") ||
    lower.endsWith(".tsx") ||
    lower.endsWith(".js") ||
    lower.endsWith(".jsx") ||
    lower.endsWith(".mjs") ||
    lower.endsWith(".cjs") ||
    lower.endsWith(".mts")
  ) {
    return "js-family";
  }
  return "other";
}

interface PortableAbsolutePath {
  root: string;
  segments: string[];
  caseInsensitive: boolean;
}

function normalizePathSegments(segments: string[]): string[] {
  const normalized: string[] = [];
  for (const segment of segments) {
    if (!segment || segment === ".") continue;
    if (segment === "..") {
      normalized.pop();
      continue;
    }
    normalized.push(segment);
  }
  return normalized;
}

function parsePortableAbsolutePath(filePath: string): PortableAbsolutePath | null {
  let normalized = filePath.trim().replace(/\\/g, "/");
  if (/^file:\/\//i.test(normalized)) {
    normalized = normalized.replace(/^file:\/\//i, "");
  }
  if (/^\/[A-Za-z]:\//.test(normalized)) normalized = normalized.slice(1);

  const drive = normalized.match(/^([A-Za-z]:)\/(.*)$/);
  if (drive) {
    return {
      root: drive[1].toLowerCase(),
      segments: normalizePathSegments(drive[2].split("/")),
      caseInsensitive: true,
    };
  }

  const unc = normalized.match(/^\/\/([^/]+)\/([^/]+)(?:\/(.*))?$/);
  if (unc) {
    return {
      root: `//${unc[1].toLowerCase()}/${unc[2].toLowerCase()}`,
      segments: normalizePathSegments((unc[3] ?? "").split("/")),
      caseInsensitive: true,
    };
  }

  if (!normalized.startsWith("/")) return null;
  return {
    root: "/",
    segments: normalizePathSegments(normalized.slice(1).split("/")),
    caseInsensitive: false,
  };
}

function portableSegmentsEqual(
  left: string,
  right: string,
  caseInsensitive: boolean,
): boolean {
  return caseInsensitive
    ? left.toLowerCase() === right.toLowerCase()
    : left === right;
}

function isSamePortableDirectory(leftPath: string, rightPath: string): boolean {
  const left = parsePortableAbsolutePath(leftPath);
  const right = parsePortableAbsolutePath(rightPath);
  if (!left || !right || left.root !== right.root) return false;
  const leftDirectory = left.segments.slice(0, -1);
  const rightDirectory = right.segments.slice(0, -1);
  if (leftDirectory.length !== rightDirectory.length) return false;
  const caseInsensitive = left.caseInsensitive || right.caseInsensitive;
  return leftDirectory.every((segment, index) =>
    portableSegmentsEqual(segment, rightDirectory[index], caseInsensitive),
  );
}

function resolveJSImportPath(
  symbol: IndexedSymbol,
  currentFilePath: string,
): string {
  const current = parsePortableAbsolutePath(currentFilePath);
  const target = parsePortableAbsolutePath(symbol.filePath);
  if (!current || !target || current.root !== target.root) {
    return symbol.exportPath;
  }

  const fromDirectory = current.segments.slice(0, -1);
  const targetSegments = [...target.segments];
  if (targetSegments.length === 0) return symbol.exportPath;
  targetSegments[targetSegments.length - 1] = targetSegments[
    targetSegments.length - 1
  ].replace(/\.(?:[cm]?[jt]sx?)$/i, "");

  const caseInsensitive = current.caseInsensitive || target.caseInsensitive;
  let commonLength = 0;
  while (
    commonLength < fromDirectory.length &&
    commonLength < targetSegments.length &&
    portableSegmentsEqual(
      fromDirectory[commonLength],
      targetSegments[commonLength],
      caseInsensitive,
    )
  ) {
    commonLength++;
  }

  const relativeSegments = [
    ...Array(fromDirectory.length - commonLength).fill(".."),
    ...targetSegments.slice(commonLength),
  ];
  if (relativeSegments.length === 0) return symbol.exportPath;
  const relativePath = relativeSegments.join("/");
  return relativePath.startsWith("..") ? relativePath : `./${relativePath}`;
}

const GO_KEYWORDS = new Set([
  "break",
  "default",
  "func",
  "interface",
  "select",
  "case",
  "defer",
  "go",
  "map",
  "struct",
  "chan",
  "else",
  "goto",
  "package",
  "switch",
  "const",
  "fallthrough",
  "if",
  "range",
  "type",
  "continue",
  "for",
  "import",
  "return",
  "var",
]);

function goPackageQualifier(exportPath: string): string {
  const pathSegments = exportPath.split("/").filter(Boolean);
  let candidate = pathSegments[pathSegments.length - 1] ?? "pkg";
  if (/^v\d+$/.test(candidate) && pathSegments.length > 1) {
    candidate = pathSegments[pathSegments.length - 2];
  } else {
    // gopkg.in encodes the major version in the package segment (yaml.v3).
    candidate = candidate.replace(/[._-]v\d+$/i, "");
  }
  candidate = candidate.replace(/[^A-Za-z0-9_]/g, "_");
  if (!candidate) candidate = "pkg";
  if (/^[0-9]/.test(candidate)) candidate = `_${candidate}`;
  if (GO_KEYWORDS.has(candidate)) candidate = `${candidate}_pkg`;
  return candidate;
}

// ---------------------------------------------------------------------------
// Module system detection
// ---------------------------------------------------------------------------

/** Detect whether a file uses ESM, CommonJS, or Go imports. */
function detectModuleSystem(filePath: string, content: string): ModuleSystem {
  const lower = filePath.toLowerCase();
  if (lower.endsWith(".go")) return "go";
  // Explicit ESM extensions.
  if (lower.endsWith(".mjs") || lower.endsWith(".mts") || lower.endsWith(".ts") || lower.endsWith(".tsx")) {
    return "esm";
  }
  // Explicit CJS extension.
  if (lower.endsWith(".cjs")) return "cjs";
  // .js / .jsx — detect from content.
  // If the file uses `import` or `export` keywords, treat as ESM.
  if (/\b(?:import|export)\b/.test(content)) return "esm";
  // If it uses require(), treat as CJS.
  if (/\brequire\s*\(/.test(content)) return "cjs";
  // Default to ESM (modern projects prefer ESM).
  return "esm";
}

// ---------------------------------------------------------------------------
// SymbolKind → CompletionItemKind mapping
// ---------------------------------------------------------------------------

function symbolKindToCompletionKind(kind: number): number {
  // These are different enums despite having overlapping numeric values.
  switch (kind) {
    case 1: return 20; // File
    case 2: return 8; // Module
    case 3: return 8; // Namespace
    case 4: return 8; // Package
    case 5: return 5; // Class
    case 6: return 0; // Method
    case 7: return 9; // Property
    case 8: return 3; // Field
    case 9: return 2; // Constructor
    case 10: return 15; // Enum
    case 11: return 7; // Interface
    case 12: return 1; // Function
    case 13: return 4; // Variable
    case 14: return 14; // Constant
    case 15: return 13; // String
    case 16: return 13; // Number
    case 17: return 13; // Boolean
    case 18: return 13; // Array
    case 19: return 6; // Object
    case 20: return 9; // Key
    case 21: return 13; // Null
    case 22: return 16; // EnumMember
    case 23: return 6; // Struct
    case 24: return 10; // Event
    case 25: return 11; // Operator
    case 26: return 24; // TypeParameter
    default: return 4; // Variable (safe default)
  }
}

// ---------------------------------------------------------------------------
// Import statement construction
// ---------------------------------------------------------------------------

/**
 * Build the import statement text for a symbol from a different file.
 * Returns the full import line (including trailing newline).
 */
function buildImportStatement(
  symbol: IndexedSymbol,
  moduleSystem: ModuleSystem,
  currentContent: string,
  resolvedImportPath = symbol.exportPath,
): string {
  const importPath = resolvedImportPath;
  if (moduleSystem === "go") {
    // Go imports are added to the import block, not as standalone lines.
    // We return just the quoted import path; the insertion logic handles
    // placing it inside the import block.
    return `"${importPath}"`;
  }
  if (moduleSystem === "cjs") {
    if (symbol.isDefaultExport) {
      return `const ${symbol.name} = require('${importPath}');\n`;
    }
    return `const { ${symbol.name} } = require('${importPath}');\n`;
  }
  // ESM
  if (symbol.isDefaultExport) {
    return `import ${symbol.name} from '${importPath}';\n`;
  }
  return `import { ${symbol.name} } from '${importPath}';\n`;
}

// ---------------------------------------------------------------------------
// Import insertion position
// ---------------------------------------------------------------------------

function isStaticJSImportStart(line: string): boolean {
  const trimmed = line.trim();
  return /^import\b/.test(trimmed) && !/^import\s*\(/.test(trimmed);
}

function isCompleteStaticJSImportDeclaration(source: string): boolean {
  const declaration = source.trim();
  if (/^import\s*["'][^"'\r\n]+["']\s*;?\s*(?:\/\/.*)?$/.test(declaration)) {
    return true;
  }
  if (
    /^import\b[\s\S]*\bfrom\s*["'][^"'\r\n]+["']\s*;?\s*(?:\/\/.*)?$/.test(
      declaration,
    )
  ) {
    return true;
  }
  return /^import\s+[A-Za-z_$][\w$]*\s*=\s*require\s*\(\s*["'][^"'\r\n]+["']\s*\)\s*;?\s*$/.test(
    declaration,
  );
}

function isDirectiveStatement(line: string): boolean {
  return /^(?:"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*')\s*;?\s*(?:\/\/.*)?$/.test(
    line.trim(),
  );
}

function isTopLevelCJSRequire(line: string): boolean {
  return (
    /^(?:const|let|var)\b/.test(line.trim()) &&
    /\brequire\s*\(/.test(line)
  );
}

/**
 * Find the position (1-based line, column) where the import should be
 * inserted. For JS/TS, this is after the last existing import (or at the top).
 * For Go, this is inside the import block (respecting alphabetical order).
 */
function findImportInsertPosition(
  content: string,
  moduleSystem: ModuleSystem,
): { line: number; column: number } {
  const lines = content.split("\n");

  if (moduleSystem === "go") {
    // Find the import block. Look for `import (` or single `import "..."`.
    let inImportBlock = false;
    let blockEnd = -1;
    let lastSingleImport = -1;

    for (let i = 0; i < lines.length; i++) {
      const trimmed = lines[i].trim();
      if (trimmed === "import (") {
        inImportBlock = true;
        continue;
      }
      if (inImportBlock && trimmed === ")") {
        inImportBlock = false;
        blockEnd = i;
        break;
      }
      if (!inImportBlock && trimmed.startsWith("import ") && trimmed.includes('"')) {
        lastSingleImport = i;
      }
    }

    if (blockEnd >= 0) {
      // Insert before the closing `)` of the import block.
      return { line: blockEnd + 1, column: 1 };
    }
    if (lastSingleImport >= 0) {
      // Convert single import to a block after the package declaration.
      // Insert a new block after the single import line.
      return { line: lastSingleImport + 2, column: 1 };
    }
    // No import block — insert after `package ...` line.
    for (let i = 0; i < lines.length; i++) {
      if (lines[i].trim().startsWith("package ")) {
        // Insert two lines after: blank line + import block.
        return { line: i + 2, column: 1 };
      }
    }
    return { line: 1, column: 1 };
  }

  // JS/TS: scan only the file preamble. Tracking the whole declaration
  // prevents insertion into a multiline named/type import, while stopping at
  // the first code statement avoids treating function-body require() calls as
  // top-level imports.
  let lastImportLine = -1;
  let i = 0;
  if (lines[0]?.startsWith("#!")) {
    lastImportLine = 0;
    i = 1;
  }

  let scanningDirectives = true;
  let seenImport = false;
  let inBlockComment = false;
  for (; i < lines.length; i++) {
    const trimmed = lines[i].trim();

    if (inBlockComment) {
      if (!seenImport) lastImportLine = i;
      if (trimmed.includes("*/")) inBlockComment = false;
      continue;
    }
    if (trimmed.startsWith("/*")) {
      if (!seenImport) lastImportLine = i;
      inBlockComment = !trimmed.includes("*/");
      continue;
    }
    if (trimmed.startsWith("//")) {
      if (!seenImport) lastImportLine = i;
      continue;
    }
    if (!trimmed) continue;

    if (scanningDirectives && isDirectiveStatement(trimmed)) {
      lastImportLine = i;
      continue;
    }
    scanningDirectives = false;

    if (moduleSystem === "esm" && isStaticJSImportStart(lines[i])) {
      let declaration = lines[i];
      let declarationEnd = i;
      while (
        !isCompleteStaticJSImportDeclaration(declaration) &&
        declarationEnd + 1 < lines.length
      ) {
        declarationEnd++;
        declaration += `\n${lines[declarationEnd]}`;
      }
      if (!isCompleteStaticJSImportDeclaration(declaration)) break;
      lastImportLine = declarationEnd;
      seenImport = true;
      i = declarationEnd;
      continue;
    }

    if (moduleSystem === "cjs" && isTopLevelCJSRequire(lines[i])) {
      lastImportLine = i;
      seenImport = true;
      continue;
    }

    break;
  }

  if (lastImportLine >= 0) {
    // Insert after the last import/directive.
    return { line: lastImportLine + 2, column: 1 };
  }

  // No imports — insert at the very top.
  return { line: 1, column: 1 };
}

/**
 * For Go, wrap the import path in a proper import block insertion.
 * Returns the full text to insert at the computed position.
 */
interface GoImportBinding {
  alias: string | null;
}

function parseGoImportSpec(
  source: string,
  importPath: string,
): GoImportBinding | null {
  const match = source.trim().match(
    new RegExp(
      `^(?:(\\.|_|[A-Za-z_][A-Za-z0-9_]*)\\s+)?"${escapeRegex(importPath)}"(?:\\s*//.*)?$`,
    ),
  );
  return match ? { alias: match[1] ?? null } : null;
}

function findGoImportBinding(
  content: string,
  importPath: string,
): GoImportBinding | null {
  const lines = content.split("\n");
  let inImportBlock = false;
  for (const line of lines) {
    const trimmed = line.trim();
    if (/^import\s*\($/.test(trimmed)) {
      inImportBlock = true;
      continue;
    }
    if (inImportBlock && trimmed === ")") {
      inImportBlock = false;
      continue;
    }
    if (inImportBlock) {
      const binding = parseGoImportSpec(trimmed, importPath);
      if (binding) return binding;
      continue;
    }
    const singleImport = trimmed.match(/^import\s+(.+)$/);
    if (singleImport) {
      const binding = parseGoImportSpec(singleImport[1], importPath);
      if (binding) return binding;
    }
  }
  return null;
}

function buildGoImportEdit(
  symbol: IndexedSymbol,
  content: string,
  importAlias?: string,
): { text: string; line: number } {
  const lines = content.split("\n");
  const importPath = symbol.exportPath;
  if (findGoImportBinding(content, importPath)) {
    return { text: "", line: -1 };
  }

  // Find existing import block.
  let inImportBlock = false;
  let blockEnd = -1;

  for (let i = 0; i < lines.length; i++) {
    const trimmed = lines[i].trim();
    if (trimmed === "import (") {
      inImportBlock = true;
      continue;
    }
    if (inImportBlock && trimmed === ")") {
      blockEnd = i;
      break;
    }
  }

  const importSpec = importAlias
    ? `${importAlias} "${importPath}"`
    : `"${importPath}"`;
  const importLine = `\t${importSpec}`;

  if (blockEnd >= 0) {
    return { text: importLine + "\n", line: blockEnd };
  }

  // No block — create one after the package declaration.
  let pkgLine = -1;
  for (let i = 0; i < lines.length; i++) {
    if (lines[i].trim().startsWith("package ")) {
      pkgLine = i;
      break;
    }
  }
  if (pkgLine >= 0) {
    const block = `\nimport (\n\t${importSpec}\n)\n`;
    return { text: block, line: pkgLine + 1 };
  }

  return { text: `import (\n\t${importSpec}\n)\n`, line: 0 };
}

// ---------------------------------------------------------------------------
// Deduplication
// ---------------------------------------------------------------------------

/**
 * Check if a symbol is already imported or defined in the current file content.
 * Returns true if the import would be redundant.
 */
function isSymbolAlreadyImported(
  symbol: IndexedSymbol,
  content: string,
  moduleSystem: ModuleSystem,
  resolvedImportPath = symbol.exportPath,
): boolean {
  const importPath = resolvedImportPath;

  if (moduleSystem === "go") {
    return findGoImportBinding(content, importPath) !== null;
  }

  if (symbol.isDefaultExport) {
    const cjsDefaultRegex = new RegExp(
      `(?:const|let|var)\\s+${escapeRegex(symbol.name)}\\s*=\\s*require\\(\\s*['"]${escapeRegex(importPath)}['"]\\s*\\)`,
    );
    if (cjsDefaultRegex.test(content)) return true;
  }

  // Check for ESM/CJS imports from the same path.
  // Simple heuristic: if the content contains the importPath in an import statement.
  const patterns = [
    `from '${importPath}'`,
    `from "${importPath}"`,
    `require('${importPath}')`,
    `require("${importPath}")`,
  ];
  for (const p of patterns) {
    if (content.includes(p)) {
      // The module is already imported. Check if the specific symbol is imported.
      if (symbol.isDefaultExport) {
        // Default ESM/CJS import bound to the requested local name.
        const importRegex = new RegExp(
          `import\\s+(\\w+)\\s+from\\s+['"]${escapeRegex(importPath)}['"]`,
        );
        const match = content.match(importRegex);
        if (match) return true;
      } else {
        // Named import — check if the symbol name is in the destructured list.
        const importRegex = new RegExp(
          `import\\s+\\{[^}]*\\b${escapeRegex(symbol.name)}\\b[^}]*\\}\\s+from\\s+['"]${escapeRegex(importPath)}['"]`,
        );
        if (importRegex.test(content)) return true;
        // CJS destructured
        const cjsRegex = new RegExp(
          `\\{[^}]*\\b${escapeRegex(symbol.name)}\\b[^}]*\\}\\s*=\\s*require\\(['"]${escapeRegex(importPath)}['"]\\)`,
        );
        if (cjsRegex.test(content)) return true;
      }
    }
  }
  return false;
}

function escapeRegex(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

// ---------------------------------------------------------------------------
// Sort text computation (mirrors lspCompletion.ts for consistency)
// ---------------------------------------------------------------------------

function computeSortText(label: string, typedWord: string): string {
  if (!typedWord) return "2";
  const lower = label.toLowerCase();
  const typed = typedWord.toLowerCase();
  if (lower === typed) return "0";
  if (lower.startsWith(typed)) return "1";
  if (lower.includes(typed)) return "2";
  return "3";
}

// ---------------------------------------------------------------------------
// Main public API
// ---------------------------------------------------------------------------

/**
 * Query the symbol index for auto-import candidates matching `typedWord`
 * and convert them to Monaco completion suggestions with additionalTextEdits
 * that insert the import statement.
 *
 * @param typedWord      The word the user is currently typing.
 * @param currentFilePath Absolute path of the file being edited.
 * @param currentContent  Full text content of the file being edited.
 * @param language       Monaco language id ("go" | "typescript" | "javascript" | ...).
 * @returns Array of auto-import suggestions (may be empty).
 */
export async function getAutoImportCompletions(
  typedWord: string,
  currentFilePath: string,
  currentContent: string,
  language: string,
): Promise<AutoImportSuggestion[]> {
  if (!typedWord || typedWord.length < 1) return [];

  // Only auto-import for go/typescript/javascript.
  const lower = currentFilePath.toLowerCase();
  const isGo =
    language === "go" || lower.endsWith(".go");
  const isTS =
    language === "typescript" ||
    language === "typescriptreact" ||
    lower.endsWith(".ts") ||
    lower.endsWith(".tsx");
  const isJS =
    language === "javascript" ||
    language === "javascriptreact" ||
    lower.endsWith(".js") ||
    lower.endsWith(".jsx") ||
    lower.endsWith(".mjs") ||
    lower.endsWith(".cjs");
  if (!isGo && !isTS && !isJS) return [];

  const moduleSystem = detectModuleSystem(currentFilePath, currentContent);

  let candidates: IndexedSymbol[];
  try {
    candidates = await symbolIndexService.getAutoImportCandidates(
      typedWord,
      currentFilePath,
    );
  } catch (error) {
    console.warn("[autoImport] getAutoImportCandidates failed:", error);
    return [];
  }
  if (!candidates || candidates.length === 0) return [];

  // 跨语言隔离（对标 VSCode / IDEA）：auto-import 只应返回与当前文件同语言的
  // 符号。后端 symbol index 将 .go/.ts/.js 混在一个索引里，这里按文件扩展名
  // 过滤，避免在 .go 文件中看到 JS/TS 符号，反之亦然。
  const currentLangGroup = languageGroupOf(currentFilePath);
  candidates = candidates.filter(
    (sym) => languageGroupOf(sym.filePath) === currentLangGroup,
  );
  if (candidates.length === 0) return [];

  const suggestions: AutoImportSuggestion[] = [];

  for (const sym of candidates) {
    if (moduleSystem === "go") {
      // Go methods require a receiver and cannot be referenced as pkg.Method.
      // Files in the same directory are the same Go package and need no import.
      if (sym.kind === 6 || isSamePortableDirectory(currentFilePath, sym.filePath)) {
        continue;
      }

      // IndexedSymbol has no package-name field. New imports therefore use an
      // explicit, legal alias derived from the import path, and the completion
      // inserts that exact alias. Existing explicit aliases are preserved;
      // existing unaliased imports still require this best-effort path heuristic.
      const fallbackQualifier = goPackageQualifier(sym.exportPath);
      const existingBinding = findGoImportBinding(
        currentContent,
        sym.exportPath,
      );
      if (existingBinding?.alias === "_") continue;
      const qualifier =
        existingBinding?.alias === "."
          ? ""
          : (existingBinding?.alias ?? fallbackQualifier);
      const goEdit = existingBinding
        ? null
        : buildGoImportEdit(sym, currentContent, fallbackQualifier);
      if (goEdit?.line === -1) continue;
      const insertText = qualifier ? `${qualifier}.${sym.name}` : sym.name;
      suggestions.push({
        label: sym.name,
        kind: symbolKindToCompletionKind(sym.kind),
        detail: `auto-import from ${sym.exportPath}`,
        insertText,
        sortText: computeSortText(sym.name, typedWord),
        documentation: sym.detail || `Import ${sym.name} from ${sym.exportPath}`,
        additionalTextEdits: goEdit
          ? [
              {
                range: {
                  startLineNumber: goEdit.line + 1,
                  startColumn: 1,
                  endLineNumber: goEdit.line + 1,
                  endColumn: 1,
                },
                text: goEdit.text,
              },
            ]
          : [],
      });
      continue;
    }

    const importPath = resolveJSImportPath(sym, currentFilePath);
    if (
      isSymbolAlreadyImported(
        sym,
        currentContent,
        moduleSystem,
        importPath,
      )
    ) {
      continue;
    }

    // JS/TS: build the import statement and find insertion position.
    const importStatement = buildImportStatement(
      sym,
      moduleSystem,
      currentContent,
      importPath,
    );
    const insertPos = findImportInsertPosition(currentContent, moduleSystem);

    suggestions.push({
      label: sym.name,
      kind: symbolKindToCompletionKind(sym.kind),
      detail: `auto-import from ${importPath}`,
      insertText: sym.name,
      sortText: computeSortText(sym.name, typedWord),
      documentation: sym.detail || `Import ${sym.name} from ${importPath}`,
      additionalTextEdits: [
        {
          range: {
            startLineNumber: insertPos.line,
            startColumn: insertPos.column,
            endLineNumber: insertPos.line,
            endColumn: insertPos.column,
          },
          text: importStatement,
        },
      ],
    });
  }

  return suggestions;
}

/**
 * Convert AutoImportSuggestion[] to Monaco CompletionItem[] and merge
 * with existing LSP suggestions. LSP labels still win, while auto-import
 * candidates with the same label remain distinct when they come from
 * different modules.
 */
export function mergeAutoImportSuggestions(
  lspSuggestions: monacoEditor.languages.CompletionItem[],
  autoImportSuggestions: AutoImportSuggestion[],
  range: {
    startLineNumber: number;
    endLineNumber: number;
    startColumn: number;
    endColumn: number;
  },
): monacoEditor.languages.CompletionItem[] {
  const lspLabels = new Set(
    lspSuggestions.map((s) =>
      typeof s.label === "string" ? s.label : s.label.label,
    ),
  );
  const seenAutoSources = new Set<string>();
  const merged = [...lspSuggestions];

  for (const auto of autoImportSuggestions) {
    if (lspLabels.has(auto.label)) continue;
    const editKey = auto.additionalTextEdits.map((edit) => edit.text).join("\u0001");
    const sourceKey = `${auto.label}\u0000${auto.detail}\u0000${auto.insertText}\u0000${editKey}`;
    if (seenAutoSources.has(sourceKey)) continue;
    seenAutoSources.add(sourceKey);

    merged.push({
      label: auto.label,
      kind: auto.kind as monacoEditor.languages.CompletionItemKind,
      detail: auto.detail,
      insertText: auto.insertText,
      insertTextRules: 0,
      range,
      sortText: auto.sortText,
      filterText: auto.label,
      documentation: auto.documentation,
      additionalTextEdits: auto.additionalTextEdits,
    });
  }

  return merged;
}

// ---------------------------------------------------------------------------
// Exports for testing
// ---------------------------------------------------------------------------

export const _internal = {
  detectModuleSystem,
  buildImportStatement,
  findImportInsertPosition,
  isSymbolAlreadyImported,
  symbolKindToCompletionKind,
  buildGoImportEdit,
  findGoImportBinding,
  goPackageQualifier,
  isCompleteStaticJSImportDeclaration,
  isSamePortableDirectory,
  resolveJSImportPath,
  escapeRegex,
};
