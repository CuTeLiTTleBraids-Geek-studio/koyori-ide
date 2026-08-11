// Koyori IDE 模块 · Search。
// 喵，这是 Koyori IDE 的 Search 模块（前端实现）~
import * as SearchServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/searchservice.js";
import type { ReplacePreview, SearchResult, StructuralReplaceEdit } from "@/types";
import { requireNonNull, unwrapNullable } from "./boundary";

type BindingSearchResult = NonNullable<
  Awaited<ReturnType<typeof SearchServiceBindings.SearchWithGlobs>>
>[number];

function fromBindingSearchResult(result: BindingSearchResult): SearchResult {
  return {
    ...result,
    matches: result.matches ?? [],
  };
}

export const searchService = {
  search: (
    root: string,
    query: string,
    ignoreCase: boolean,
    includeGlobs: string[] = [],
    excludeGlobs: string[] = [],
  ) => unwrapNullable(
    SearchServiceBindings.SearchWithGlobs(
      root,
      query,
      ignoreCase,
      includeGlobs,
      excludeGlobs,
    ),
    [],
  ).then((results) => results.map(fromBindingSearchResult)),
  replace: (filePath: string, pattern: string, replacement: string, caseSensitive: boolean) =>
    requireNonNull(
      SearchServiceBindings.Replace(filePath, pattern, replacement, caseSensitive),
      "SearchService.Replace",
    ),
  previewReplace: (filePath: string, pattern: string, replacement: string, caseSensitive: boolean) =>
    requireNonNull(
      SearchServiceBindings.PreviewReplace(filePath, pattern, replacement, caseSensitive),
      "SearchService.PreviewReplace",
    ),
  applyReplacePreview: (
    filePath: string,
    expectedHash: string,
    pattern: string,
    replacement: string,
    caseSensitive: boolean,
  ) => requireNonNull(
    SearchServiceBindings.ApplyReplacePreview(
      filePath,
      expectedHash,
      pattern,
      replacement,
      caseSensitive,
    ),
    "SearchService.ApplyReplacePreview",
  ),
  applyMultiFileReplace: (previews: ReplacePreview[]) =>
    SearchServiceBindings.ApplyMultiFileReplace(previews),
  previewStructuralReplace: (filePath: string, edits: StructuralReplaceEdit[]) =>
    requireNonNull(
      SearchServiceBindings.PreviewStructuralReplace(filePath, edits),
      "SearchService.PreviewStructuralReplace",
    ),
  applyStructuralReplacePreview: (
    filePath: string,
    expectedHash: string,
    edits: StructuralReplaceEdit[],
  ) => requireNonNull(
    SearchServiceBindings.ApplyStructuralReplacePreview(filePath, expectedHash, edits),
    "SearchService.ApplyStructuralReplacePreview",
  ),
};
