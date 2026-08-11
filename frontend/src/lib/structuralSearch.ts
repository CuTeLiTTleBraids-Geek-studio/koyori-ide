// Koyori IDE 模块 · Structural Search。
// 喵，这是 Koyori IDE 的 Structural Search 模块（前端实现）~
import type { LSPDocumentSymbol, LSPRange } from "@/types";

export interface StructuralPatternSegment {
  kind: number | null;
  namePattern: string;
}

export interface StructuralSymbolMatch {
  path: string;
  name: string;
  detail?: string;
  kind: number;
  kindLabel: string;
  symbolPath: string[];
  selectionRange: LSPRange;
}

const symbolKindLabels = [
  "",
  "file",
  "module",
  "namespace",
  "package",
  "class",
  "method",
  "property",
  "field",
  "constructor",
  "enum",
  "interface",
  "function",
  "variable",
  "constant",
  "string",
  "number",
  "boolean",
  "array",
  "object",
  "key",
  "null",
  "enumMember",
  "struct",
  "event",
  "operator",
  "typeParameter",
] as const;

const kindByLabel = new Map<string, number>(
  symbolKindLabels.slice(1).map((label, index) => [label.toLowerCase(), index + 1]),
);

export function structuralSymbolKindLabel(kind: number): string {
  return symbolKindLabels[kind] ?? "symbol";
}

export function parseStructuralPattern(input: string): StructuralPatternSegment[] {
  const trimmed = input.trim();
  if (!trimmed) throw new Error("structural pattern cannot be empty");
  if (trimmed.length > 1024) throw new Error("structural pattern is too long");
  const rawSegments = trimmed.split(">");
  if (rawSegments.length > 16) throw new Error("structural pattern has too many segments");
  return rawSegments.map((raw) => {
    const segment = raw.trim();
    if (!segment) throw new Error("structural pattern contains an empty segment");
    const separator = segment.indexOf(":");
    if (separator < 0) return { kind: null, namePattern: segment };
    const kindLabel = segment.slice(0, separator).trim().toLowerCase();
    const namePattern = segment.slice(separator + 1).trim();
    const kind = kindByLabel.get(kindLabel);
    if (!kind) throw new Error(`unknown symbol kind: ${kindLabel || "(empty)"}`);
    if (!namePattern) throw new Error("structural symbol name pattern cannot be empty");
    return { kind, namePattern };
  });
}

function globRegex(pattern: string, caseSensitive: boolean): RegExp {
  let source = "^";
  for (const char of pattern) {
    if (char === "*") source += ".*";
    else if (char === "?") source += ".";
    else source += char.replace(/[\\^$.*+?()[\]{}|]/g, "\\$&");
  }
  source += "$";
  return new RegExp(source, caseSensitive ? "u" : "iu");
}

function fileGlobRegex(glob: string): RegExp {
  const normalized = glob.trim().replace(/\\/g, "/").replace(/^\.\//, "");
  if (!normalized) throw new Error("file glob cannot be empty");
  let source = "^";
  for (let index = 0; index < normalized.length; index += 1) {
    const char = normalized[index];
    if (char === "*" && normalized[index + 1] === "*") {
      index += 1;
      if (normalized[index + 1] === "/") {
        index += 1;
        source += "(?:.*/)?";
      } else {
        source += ".*";
      }
    } else if (char === "*") {
      source += "[^/]*";
    } else if (char === "?") {
      source += "[^/]";
    } else {
      source += char.replace(/[\\^$.*+?()[\]{}|]/g, "\\$&");
    }
  }
  return new RegExp(`${source}$`, "u");
}

export function matchesStructuralFileGlobs(
  path: string,
  includeGlobs: string[],
  excludeGlobs: string[],
): boolean {
  const normalizedPath = path.replace(/\\/g, "/").replace(/^\.\//, "");
  const includes = includeGlobs.map(fileGlobRegex);
  const excludes = excludeGlobs.map(fileGlobRegex);
  if (includes.length > 0 && !includes.some((pattern) => pattern.test(normalizedPath))) return false;
  return !excludes.some((pattern) => pattern.test(normalizedPath));
}

function chainMatches(
  chain: LSPDocumentSymbol[],
  pattern: StructuralPatternSegment[],
  caseSensitive: boolean,
): boolean {
  if (chain.length < pattern.length) return false;
  const offset = chain.length - pattern.length;
  return pattern.every((segment, index) => {
    const symbol = chain[offset + index];
    return (segment.kind === null || segment.kind === symbol.kind)
      && globRegex(segment.namePattern, caseSensitive).test(symbol.name);
  });
}

export function searchDocumentSymbols(
  path: string,
  symbols: LSPDocumentSymbol[],
  pattern: StructuralPatternSegment[],
  caseSensitive: boolean,
): StructuralSymbolMatch[] {
  const results: StructuralSymbolMatch[] = [];
  function visit(items: LSPDocumentSymbol[], ancestors: LSPDocumentSymbol[]): void {
    for (const symbol of items) {
      const chain = [...ancestors, symbol];
      if (chainMatches(chain, pattern, caseSensitive)) {
        results.push({
          path,
          name: symbol.name,
          detail: symbol.detail,
          kind: symbol.kind,
          kindLabel: structuralSymbolKindLabel(symbol.kind),
          symbolPath: chain.map((item) => item.name),
          selectionRange: symbol.selectionRange,
        });
      }
      if (symbol.children?.length) visit(symbol.children, chain);
    }
  }
  visit(symbols, []);
  return results;
}
