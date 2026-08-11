import { describe, it, expect } from "vitest";
import {
  matchIndices,
  scoreMatch,
  fuzzyFilter,
  basename,
  dirname,
} from "./fuzzy";

describe("matchIndices", () => {
  it("returns empty array for empty query", () => {
    expect(matchIndices("foo.ts", "")).toEqual([]);
  });

  it("returns indices for a subsequence match", () => {
    expect(matchIndices("foo.ts", "ft")).toEqual([0, 4]);
  });

  it("is case-insensitive", () => {
    expect(matchIndices("Foo.Ts", "ft")).toEqual([0, 4]);
  });

  it("returns null when query is not a subsequence", () => {
    expect(matchIndices("foo.ts", "xyz")).toBeNull();
  });

  it("returns null when query is longer than path", () => {
    expect(matchIndices("ab", "abc")).toBeNull();
  });

  it("matches identical strings", () => {
    expect(matchIndices("foo", "foo")).toEqual([0, 1, 2]);
  });

  it("matches out-of-order characters in query (subsequence, not substring)", () => {
    // "tsf" is NOT a subsequence of "foo.ts" because 'f' appears before 't'
    // in the path and 's' appears after 't'. The query "tsf" requires
    // t then s then f in order — "foo.ts" has t at index 4, s at index 5,
    // but no f after index 5. So this should be null.
    expect(matchIndices("foo.ts", "tsf")).toBeNull();
  });
});

describe("scoreMatch", () => {
  it("returns 1 for empty indices (empty query)", () => {
    expect(scoreMatch("foo.ts", [])).toBe(1);
  });

  it("gives higher score for matches at segment boundaries", () => {
    // Matching "f" at start of "foo/bar.ts" (boundary) should score higher
    // than matching "f" inside "bar" (non-boundary).
    const boundaryScore = scoreMatch("foo/bar.ts", [0]); // "f" at start
    const interiorScore = scoreMatch("foo/bar.ts", [5]); // "a" inside "bar"
    expect(boundaryScore).toBeGreaterThan(interiorScore);
  });

  it("gives higher score for consecutive matches", () => {
    const consecutive = scoreMatch("abc", [0, 1, 2]);
    const spread = scoreMatch("axbxc", [0, 2, 4]);
    expect(consecutive).toBeGreaterThan(spread);
  });

  it("gives higher score for filename matches than directory matches", () => {
    // "foo.ts" — matching "f" in filename (index 4) vs matching "s" in dir (index 2)
    const filenameScore = scoreMatch("src/foo.ts", [4]); // "f" in "foo.ts"
    const dirScore = scoreMatch("src/foo.ts", [0]); // "s" in "src"
    expect(filenameScore).toBeGreaterThan(dirScore);
  });

  it("penalizes longer paths", () => {
    const short = scoreMatch("a", [0]);
    const long = scoreMatch("a" + "x".repeat(50), [0]);
    expect(short).toBeGreaterThan(long);
  });
});

describe("fuzzyFilter", () => {
  it("returns all paths (up to limit) for empty query", () => {
    const paths = ["a.ts", "b.ts", "c.ts"];
    const result = fuzzyFilter(paths, "");
    expect(result).toHaveLength(3);
    expect(result.map((r) => r.path)).toEqual(paths);
  });

  it("respects the limit for empty query", () => {
    const paths = Array.from({ length: 10 }, (_, i) => `f${i}.ts`);
    const result = fuzzyFilter(paths, "", 5);
    expect(result).toHaveLength(5);
  });

  it("filters out non-matching paths", () => {
    const paths = ["foo.ts", "bar.go", "baz.py"];
    const result = fuzzyFilter(paths, "foo");
    expect(result).toHaveLength(1);
    expect(result[0].path).toBe("foo.ts");
  });

  it("ranks better matches higher", () => {
    const paths = [
      "src/util/foo.ts",        // "foo" in filename
      "src/foo_helper/handle.ts", // "foo" at segment boundary
      "xfoo/bar.ts",            // "foo" not at boundary
    ];
    const result = fuzzyFilter(paths, "foo");
    expect(result.length).toBe(3);
    // The filename match (src/util/foo.ts) should rank highest.
    expect(result[0].path).toBe("src/util/foo.ts");
  });

  it("respects the limit for non-empty query", () => {
    const paths = Array.from({ length: 10 }, (_, i) => `foo${i}.ts`);
    const result = fuzzyFilter(paths, "foo", 3);
    expect(result).toHaveLength(3);
  });

  it("is case-insensitive", () => {
    const paths = ["Foo.ts", "bar.go"];
    const result = fuzzyFilter(paths, "foo");
    expect(result).toHaveLength(1);
    expect(result[0].path).toBe("Foo.ts");
  });

  it("returns empty array when nothing matches", () => {
    const paths = ["foo.ts", "bar.go"];
    const result = fuzzyFilter(paths, "xyz");
    expect(result).toEqual([]);
  });

  it("tie-breaks alphabetically", () => {
    // Both "a/foo.ts" and "b/foo.ts" match "foo" with the same score
    // (same filename position, same path length). Tie-break alphabetically.
    const paths = ["b/foo.ts", "a/foo.ts"];
    const result = fuzzyFilter(paths, "foo");
    expect(result[0].path).toBe("a/foo.ts");
    expect(result[1].path).toBe("b/foo.ts");
  });
});

describe("basename", () => {
  it("returns the last segment of a path", () => {
    expect(basename("src/foo/bar.ts")).toBe("bar.ts");
  });

  it("returns the whole string when no separator", () => {
    expect(basename("foo.ts")).toBe("foo.ts");
  });

  it("returns empty string for a trailing slash", () => {
    expect(basename("src/foo/")).toBe("");
  });
});

describe("dirname", () => {
  it("returns everything before the last separator", () => {
    expect(dirname("src/foo/bar.ts")).toBe("src/foo");
  });

  it("returns empty string when no separator", () => {
    expect(dirname("foo.ts")).toBe("");
  });

  it("returns the directory part for a trailing slash", () => {
    expect(dirname("src/foo/")).toBe("src/foo");
  });
});
