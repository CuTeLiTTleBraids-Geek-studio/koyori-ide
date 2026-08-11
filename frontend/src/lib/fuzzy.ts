// Plan 55: fuzzy matcher for Quick Open.
//
// Implements a subsequence matcher with a scoring heuristic that rewards:
//   - consecutive character matches (compactness)
//   - matches at the start of a path segment (leading-char bonus)
//   - matches just after a path separator or camelCase boundary
//
// The matcher is intentionally simple — no external deps, no fancy Unicode
// handling. It's fast enough to run on 10k+ paths on every keystroke.

// Koyori IDE 模块 · Fuzzy。
// 喵，这是 Koyori IDE 的 Fuzzy 模块（前端实现）~
export interface FuzzyMatch {
  /** The original path that was matched. */
  path: string;
  /** Numeric score; higher is better. 0 means no match. */
  score: number;
}

/**
 * Returns the indices in `path` (0-based) where the query characters matched,
 * or `null` if `query` is not a subsequence of `path` (case-insensitive).
 */
export function matchIndices(path: string, query: string): number[] | null {
  if (!query) return [];
  const p = path.toLowerCase();
  const q = query.toLowerCase();
  const indices: number[] = [];
  let qi = 0;
  for (let pi = 0; pi < p.length && qi < q.length; pi++) {
    if (p[pi] === q[qi]) {
      indices.push(pi);
      qi++;
    }
  }
  return qi === q.length ? indices : null;
}

/**
 * Scores a match. Higher is better. The score rewards:
 *   - leading-char matches (at start of string or after a separator)
 *   - consecutive matches (compactness)
 *   - matching the filename (last segment) more than the directory path
 *
 * @param path the original path (with forward slashes)
 * @param indices the matched character indices (from matchIndices)
 */
export function scoreMatch(path: string, indices: number[]): number {
  if (indices.length === 0) return 1; // empty query — neutral score
  let score = 0;
  let prev = -2; // force the first char to count as a "boundary"
  // Find where the filename starts (last "/").
  const filenameStart = path.lastIndexOf("/") + 1;
  for (const i of indices) {
    // Boundary bonus: char is at start of string, or follows "/" or "_",
    // or is an uppercase letter following a lowercase letter (camelCase).
    const isBoundary =
      i === 0 ||
      path[i - 1] === "/" ||
      path[i - 1] === "_" ||
      path[i - 1] === "-" ||
      path[i - 1] === "." ||
      (i > 0 && path[i] >= "A" && path[i] <= "Z" && path[i - 1] >= "a" && path[i - 1] <= "z");
    if (isBoundary) score += 8;
    else score += 1;
    // Consecutive-match bonus.
    if (i === prev + 1) score += 5;
    // Filename match bonus — matches in the filename are weighted higher
    // than matches in the directory path.
    if (i >= filenameStart) score += 3;
    prev = i;
  }
  // Slight penalty for longer paths (prefer shorter matches).
  score -= path.length * 0.05;
  return score;
}

/**
 * Filters and ranks `paths` against `query`. Returns the paths sorted by
 * score (descending). Paths that don't match are excluded. If `query` is
 * empty, returns all paths in their original order (no scoring).
 *
 * Results are capped at `limit` (default 200) to keep the UI responsive.
 */
export function fuzzyFilter(
  paths: string[],
  query: string,
  limit = 200,
): FuzzyMatch[] {
  if (!query) {
    return paths.slice(0, limit).map((path) => ({ path, score: 1 }));
  }
  const results: FuzzyMatch[] = [];
  for (const path of paths) {
    const indices = matchIndices(path, query);
    if (indices === null) continue;
    results.push({ path, score: scoreMatch(path, indices) });
  }
  results.sort((a, b) => {
    if (b.score !== a.score) return b.score - a.score;
    // Tie-break alphabetically for stable ordering.
    return a.path < b.path ? -1 : a.path > b.path ? 1 : 0;
  });
  return results.slice(0, limit);
}

/**
 * Returns the basename (last path segment) of a forward-slash path.
 * Used by the Quick Open UI to show the filename prominently.
 */
export function basename(path: string): string {
  const i = path.lastIndexOf("/");
  return i === -1 ? path : path.slice(i + 1);
}

/**
 * Returns the directory portion of a forward-slash path (everything before
 * the last "/"), or an empty string if the path has no separator.
 */
export function dirname(path: string): string {
  const i = path.lastIndexOf("/");
  return i === -1 ? "" : path.slice(0, i);
}
