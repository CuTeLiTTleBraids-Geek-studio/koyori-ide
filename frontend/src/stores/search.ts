// Koyori IDE 模块 · Search。
// 喵，这是 Koyori IDE 的 Search 模块（前端实现）~
import { reactive } from "vue";
import { searchService } from "@/api/services";
import { errorMessage } from "@/lib/errors";
import type { ReplacePreview, SearchResult } from "@/types";

export interface SelectableReplacePreview extends ReplacePreview {
  selected: boolean;
}

export interface SearchState {
  query: string;
  ignoreCase: boolean;
  /** G-SEARCH-01: when true, the query is treated as a regular expression. */
  useRegex: boolean;
  results: SearchResult[];
  loading: boolean;
  error: string | null;
  /** prompt-7 Task L: true when backend returned more hits than UI cap. */
  truncated: boolean;
  includeGlob: string;
  excludeGlob: string;
  replacePreviews: SelectableReplacePreview[];
  previewLoading: boolean;
  previewApplying: boolean;
  replaceConflicts: string[];
}

/** Cap UI-held search results to keep the sidebar responsive (prompt-7 Task L). */
export const MAX_SEARCH_UI_RESULTS = 500;

export const searchState = reactive<SearchState>({
  query: "",
  ignoreCase: false,
  useRegex: false,
  results: [],
  loading: false,
  error: null,
  truncated: false,
  includeGlob: "",
  excludeGlob: "",
  replacePreviews: [],
  previewLoading: false,
  previewApplying: false,
  replaceConflicts: [],
});

let debounceTimer: ReturnType<typeof setTimeout> | null = null;
let searchGeneration = 0;
let replacePreviewGeneration = 0;

function parseGlobInput(value: string): string[] {
  return value.split(",").map((glob) => glob.trim()).filter(Boolean);
}

function workspaceFilePath(root: string, relativePath: string): string {
  return `${root.replace(/[\\/]+$/, "")}/${relativePath.replace(/^[/\\]+/, "")}`;
}

export async function runSearch(root: string, query: string): Promise<void> {
  const generation = ++searchGeneration;
  if (!query.trim()) {
    searchState.results = [];
    searchState.query = query;
    searchState.truncated = false;
    return;
  }
  searchState.query = query;
  searchState.loading = true;
  searchState.error = null;
  try {
    // G-SEARCH-01: when useRegex is false, escape regex metacharacters so the
    // backend's regex-based search treats the query as plain text.
    const pattern = searchState.useRegex ? query : escapeRegex(query);
    const results = await searchService.search(
      root,
      pattern,
      searchState.ignoreCase,
      parseGlobInput(searchState.includeGlob),
      parseGlobInput(searchState.excludeGlob),
    );
    if (generation !== searchGeneration) return;
    if (results.length > MAX_SEARCH_UI_RESULTS) {
      searchState.results = results.slice(0, MAX_SEARCH_UI_RESULTS);
      searchState.truncated = true;
    } else {
      searchState.results = results;
      searchState.truncated = false;
    }
  } catch (e: unknown) {
    if (generation !== searchGeneration) return;
    searchState.error = errorMessage(e);
    searchState.results = [];
    searchState.truncated = false;
  } finally {
    if (generation === searchGeneration) {
      searchState.loading = false;
    }
  }
}

/** Escape regex metacharacters in a plain-text search query. */
function escapeRegex(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

export function debouncedSearch(root: string, query: string, delay = 300): void {
  cancelDebouncedSearch();
  debounceTimer = setTimeout(() => {
    debounceTimer = null;
    void runSearch(root, query);
  }, delay);
}

export function cancelDebouncedSearch(): void {
  if (debounceTimer) {
    clearTimeout(debounceTimer);
    debounceTimer = null;
  }
  searchGeneration += 1;
  searchState.loading = false;
}

export function clearSearch(): void {
  cancelDebouncedSearch();
  searchState.query = "";
  searchState.results = [];
  searchState.error = null;
  searchState.loading = false;
  searchState.truncated = false;
  cancelReplacePreview();
  // G-SEARCH-01: keep useRegex persistent (don't reset on clear).
}

export function cancelReplacePreview(): void {
  replacePreviewGeneration += 1;
  searchState.replacePreviews = [];
  searchState.previewLoading = false;
  searchState.previewApplying = false;
  searchState.replaceConflicts = [];
}

import.meta.hot?.dispose(() => {
  cancelDebouncedSearch();
  cancelReplacePreview();
});

export async function previewReplacements(
  repoPath: string,
  pattern: string,
  replacement: string,
  caseSensitive: boolean,
): Promise<void> {
  cancelReplacePreview();
  const generation = replacePreviewGeneration;
  searchState.previewLoading = true;
  searchState.error = null;
  try {
    const previews: SelectableReplacePreview[] = [];
    for (const result of searchState.results) {
      const preview = await searchService.previewReplace(
        workspaceFilePath(repoPath, result.path),
        pattern,
        replacement,
        caseSensitive,
      );
      if (generation !== replacePreviewGeneration) return;
      if (preview.replacements > 0) {
        previews.push({ ...preview, selected: true });
      }
    }
    if (generation === replacePreviewGeneration) {
      searchState.replacePreviews = previews;
    }
  } catch (error: unknown) {
    if (generation === replacePreviewGeneration) {
      searchState.error = errorMessage(error);
      searchState.replacePreviews = [];
    }
  } finally {
    if (generation === replacePreviewGeneration) {
      searchState.previewLoading = false;
    }
  }
}

export async function applySelectedPreviews(
  _pattern: string,
  _replacement: string,
  _caseSensitive: boolean,
): Promise<number> {
  const selected = searchState.replacePreviews.filter((preview) => preview.selected);
  if (selected.length === 0) return 0;
  searchState.previewApplying = true;
  searchState.error = null;
  searchState.replaceConflicts = [];
  try {
    const total = await applyPreviewTransaction(selected);
    if (searchState.error) return 0;
    cancelReplacePreview();
    return total;
  } catch (error: unknown) {
    searchState.error = errorMessage(error);
    return 0;
  } finally {
    searchState.previewApplying = false;
  }
}

async function applyPreviewTransaction(
  previews: Array<ReplacePreview | SelectableReplacePreview>,
): Promise<number> {
  const immutablePreviews = previews.map((preview) => ({
    path: preview.path,
    originalHash: preview.originalHash,
    originalContent: preview.originalContent,
    modifiedContent: preview.modifiedContent,
    replacements: preview.replacements,
  }));
  const result = await searchService.applyMultiFileReplace(immutablePreviews);
  if (!result.applied) {
    searchState.replaceConflicts = result.conflicts ?? [];
    searchState.error = result.failureReason || "workspace edit conflict";
    return 0;
  }
  searchState.replaceConflicts = [];
  return immutablePreviews.reduce((sum, preview) => sum + preview.replacements, 0);
}

export async function replaceInFile(repoPath: string, filePath: string, pattern: string, replacement: string, caseSensitive: boolean) {
  const fullPath = repoPath + "/" + filePath;
  return searchService.replace(fullPath, pattern, replacement, caseSensitive);
}

export async function replaceAll(repoPath: string, pattern: string, replacement: string, caseSensitive: boolean) {
  searchState.error = null;
  searchState.replaceConflicts = [];
  try {
    const previews: ReplacePreview[] = [];
    for (const result of searchState.results) {
      const preview = await searchService.previewReplace(
        workspaceFilePath(repoPath, result.path),
        pattern,
        replacement,
        caseSensitive,
      );
      if (preview.replacements > 0) previews.push(preview);
    }
    return await applyPreviewTransaction(previews);
  } catch (error: unknown) {
    searchState.error = errorMessage(error);
    return 0;
  }
}
