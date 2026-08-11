// Koyori IDE 模块 · Structural Search；交互服务：文件系统（FileService）。
// 喵，这是 Koyori IDE 的 Structural Search 模块（前端实现）~
import { reactive } from "vue";
import { fileService, searchService } from "@/api/services";
import { errorMessage } from "@/lib/errors";
import { detectLanguage } from "@/lib/language";
import {
  matchesStructuralFileGlobs,
  parseStructuralPattern,
  searchDocumentSymbols,
  type StructuralPatternSegment,
  type StructuralSymbolMatch,
} from "@/lib/structuralSearch";
import { ensureLSPRunning, getLSPDocumentSymbols, monacoLanguageToLSP } from "@/stores/lsp";
import type { ReplacePreview, StructuralReplaceEdit } from "@/types";

export interface SelectableStructuralMatch extends StructuralSymbolMatch {
  selected: boolean;
}

export interface SelectableStructuralPreview extends ReplacePreview {
  edits: StructuralReplaceEdit[];
  selected: boolean;
}

interface StructuralSearchState {
  query: string;
  replacement: string;
  results: SelectableStructuralMatch[];
  previews: SelectableStructuralPreview[];
  loading: boolean;
  previewLoading: boolean;
  applying: boolean;
  error: string | null;
  truncated: boolean;
  scannedFiles: number;
  skippedFiles: number;
}

export interface StructuralSearchOptions {
  caseSensitive: boolean;
  includeGlobs?: string[];
  excludeGlobs?: string[];
}

export const MAX_STRUCTURAL_SEARCH_FILES = 2000;
export const MAX_STRUCTURAL_SEARCH_RESULTS = 500;
const STRUCTURAL_SEARCH_CONCURRENCY = 4;

export const structuralSearchState = reactive<StructuralSearchState>({
  query: "",
  replacement: "",
  results: [],
  previews: [],
  loading: false,
  previewLoading: false,
  applying: false,
  error: null,
  truncated: false,
  scannedFiles: 0,
  skippedFiles: 0,
});

let searchGeneration = 0;
let previewGeneration = 0;

function workspaceFilePath(root: string, relativePath: string): string {
  return `${root.replace(/[\\/]+$/, "")}/${relativePath.replace(/^[\\/]+/, "")}`;
}

function structuralEdit(match: SelectableStructuralMatch, replacement: string): StructuralReplaceEdit {
  return {
    startLine: match.selectionRange.start.line,
    startCharacter: match.selectionRange.start.character,
    endLine: match.selectionRange.end.line,
    endCharacter: match.selectionRange.end.character,
    expectedText: match.name,
    replacement,
  };
}

export function parseStructuralGlobInput(value: string): string[] {
  return value.split(",").map((glob) => glob.trim()).filter(Boolean);
}

export function cancelStructuralSearch(): void {
  searchGeneration += 1;
  structuralSearchState.loading = false;
}

export function cancelStructuralPreview(): void {
  previewGeneration += 1;
  structuralSearchState.previews = [];
  structuralSearchState.previewLoading = false;
}

export function clearStructuralSearch(): void {
  cancelStructuralSearch();
  cancelStructuralPreview();
  structuralSearchState.query = "";
  structuralSearchState.replacement = "";
  structuralSearchState.results = [];
  structuralSearchState.loading = false;
  structuralSearchState.applying = false;
  structuralSearchState.error = null;
  structuralSearchState.truncated = false;
  structuralSearchState.scannedFiles = 0;
  structuralSearchState.skippedFiles = 0;
}

export async function runStructuralSearch(
  root: string,
  query: string,
  options: StructuralSearchOptions,
): Promise<void> {
  const generation = ++searchGeneration;
  cancelStructuralPreview();
  structuralSearchState.query = query;
  structuralSearchState.results = [];
  structuralSearchState.error = null;
  structuralSearchState.truncated = false;
  structuralSearchState.scannedFiles = 0;
  structuralSearchState.skippedFiles = 0;
  if (!root || !query.trim()) {
    structuralSearchState.loading = false;
    return;
  }

  let pattern: StructuralPatternSegment[];
  try {
    pattern = parseStructuralPattern(query);
  } catch (error: unknown) {
    structuralSearchState.error = errorMessage(error);
    structuralSearchState.loading = false;
    return;
  }

  structuralSearchState.loading = true;
  try {
    const files = await fileService.listAllFiles(root);
    if (generation !== searchGeneration) return;
    const includeGlobs = options.includeGlobs ?? [];
    const excludeGlobs = options.excludeGlobs ?? [];
    const candidates = files.flatMap((path) => {
      if (!matchesStructuralFileGlobs(path, includeGlobs, excludeGlobs)) return [];
      const language = monacoLanguageToLSP(detectLanguage(path));
      return language && language !== "eslint" ? [{ path, language }] : [];
    });
    const languages = [...new Set(candidates.map((candidate) => candidate.language))];
    const availability = await Promise.all(languages.map(async (language) => [
      language,
      await ensureLSPRunning(language),
    ] as const));
    if (generation !== searchGeneration) return;
    const availableLanguages = new Set(
      availability.filter(([, available]) => available).map(([language]) => language),
    );
    const availableCandidates = candidates.filter((candidate) => availableLanguages.has(candidate.language));
    const limited = availableCandidates.slice(0, MAX_STRUCTURAL_SEARCH_FILES);
    const collected: SelectableStructuralMatch[][] = Array.from({ length: limited.length }, () => []);
    let nextIndex = 0;
    let skippedFiles = candidates.length - availableCandidates.length;

    async function worker(): Promise<void> {
      while (generation === searchGeneration) {
        const index = nextIndex;
        nextIndex += 1;
        if (index >= limited.length) return;
        const candidate = limited[index];
        const fullPath = workspaceFilePath(root, candidate.path);
        try {
          const content = await fileService.readFile(fullPath);
          if (generation !== searchGeneration) return;
          const symbols = await getLSPDocumentSymbols(candidate.language, fullPath, content);
          if (generation !== searchGeneration) return;
          collected[index] = searchDocumentSymbols(
            candidate.path,
            symbols,
            pattern,
            options.caseSensitive,
          ).map((match) => ({ ...match, selected: true }));
        } catch {
          skippedFiles += 1;
        }
      }
    }

    await Promise.all(Array.from(
      { length: Math.min(STRUCTURAL_SEARCH_CONCURRENCY, limited.length) },
      () => worker(),
    ));
    if (generation !== searchGeneration) return;
    const allResults = collected.flat();
    structuralSearchState.results = allResults.slice(0, MAX_STRUCTURAL_SEARCH_RESULTS);
    structuralSearchState.truncated = availableCandidates.length > limited.length
      || allResults.length > MAX_STRUCTURAL_SEARCH_RESULTS;
    structuralSearchState.scannedFiles = limited.length;
    structuralSearchState.skippedFiles = skippedFiles;
  } catch (error: unknown) {
    if (generation === searchGeneration) {
      structuralSearchState.error = errorMessage(error);
      structuralSearchState.results = [];
    }
  } finally {
    if (generation === searchGeneration) structuralSearchState.loading = false;
  }
}

export async function previewStructuralReplacements(root: string, replacement: string): Promise<void> {
  const generation = ++previewGeneration;
  structuralSearchState.previews = [];
  structuralSearchState.error = null;
  structuralSearchState.replacement = replacement;
  if (!replacement.trim()) {
    structuralSearchState.error = "structural replacement cannot be empty";
    return;
  }
  const selected = structuralSearchState.results.filter((match) => match.selected);
  if (selected.length === 0) return;
  structuralSearchState.previewLoading = true;
  try {
    const byPath = new Map<string, SelectableStructuralMatch[]>();
    for (const match of selected) {
      const matches = byPath.get(match.path) ?? [];
      matches.push(match);
      byPath.set(match.path, matches);
    }
    const previews: SelectableStructuralPreview[] = [];
    for (const [path, matches] of byPath) {
      const edits = matches.map((match) => structuralEdit(match, replacement));
      const preview = await searchService.previewStructuralReplace(workspaceFilePath(root, path), edits);
      if (generation !== previewGeneration) return;
      previews.push({ ...preview, edits, selected: true });
    }
    structuralSearchState.previews = previews;
  } catch (error: unknown) {
    if (generation === previewGeneration) structuralSearchState.error = errorMessage(error);
  } finally {
    if (generation === previewGeneration) structuralSearchState.previewLoading = false;
  }
}

export async function applySelectedStructuralPreviews(): Promise<number> {
  const selected = structuralSearchState.previews.filter((preview) => preview.selected);
  if (selected.length === 0) return 0;
  structuralSearchState.applying = true;
  structuralSearchState.error = null;
  let total = 0;
  try {
    for (const preview of selected) {
      const result = await searchService.applyStructuralReplacePreview(
        preview.path,
        preview.originalHash,
        preview.edits,
      );
      total += result.replacements;
    }
    structuralSearchState.previews = [];
    return total;
  } catch (error: unknown) {
    structuralSearchState.error = errorMessage(error);
    return total;
  } finally {
    structuralSearchState.applying = false;
  }
}
