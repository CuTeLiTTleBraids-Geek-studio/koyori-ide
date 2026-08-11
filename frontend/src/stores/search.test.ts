import { describe, it, expect, beforeEach, vi } from "vitest";

vi.mock("@/api/services", () => ({
  searchService: {
    search: vi.fn().mockResolvedValue([
      {
        path: "a.txt",
        matches: [
          { line: 1, column: 1, preview: "hello world" },
          { line: 3, column: 1, preview: "hello again" },
        ],
      },
      {
        path: "b.ts",
        matches: [{ line: 5, column: 3, preview: "  hello there" }],
      },
    ]),
    previewReplace: vi.fn(),
    applyReplacePreview: vi.fn(),
    applyMultiFileReplace: vi.fn(),
  },
}));

import {
  searchState,
  runSearch,
  clearSearch,
  debouncedSearch,
  cancelDebouncedSearch,
  previewReplacements,
  applySelectedPreviews,
  replaceAll,
  cancelReplacePreview,
} from "./search";

describe("search store", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    searchState.query = "";
    searchState.results = [];
    searchState.loading = false;
    searchState.error = null;
    searchState.ignoreCase = false;
    searchState.includeGlob = "";
    searchState.excludeGlob = "";
    cancelReplacePreview();
  });

  it("starts with empty state", () => {
    expect(searchState.query).toBe("");
    expect(searchState.results).toHaveLength(0);
    expect(searchState.loading).toBe(false);
  });

  it("runSearch populates results", async () => {
    await runSearch("/repo", "hello");
    expect(searchState.results).toHaveLength(2);
    expect(searchState.results[0].path).toBe("a.txt");
    expect(searchState.results[0].matches).toHaveLength(2);
    expect(searchState.loading).toBe(false);
  });

  it("runSearch does nothing with empty query", async () => {
    await runSearch("/repo", "");
    expect(searchState.results).toHaveLength(0);
    const { searchService } = await import("@/api/services");
    expect(searchService.search).not.toHaveBeenCalled();
  });

  it("toggle ignoreCase is reflected in state", async () => {
    searchState.ignoreCase = true;
    await runSearch("/repo", "Hello");
    const { searchService } = await import("@/api/services");
    expect(searchService.search).toHaveBeenCalledWith("/repo", "Hello", true, [], []);
  });

  it("passes comma-separated include and exclude globs", async () => {
    searchState.includeGlob = "**/*.ts, src/**";
    searchState.excludeGlob = "**/*.test.ts";
    await runSearch("/repo", "hello");
    const { searchService } = await import("@/api/services");
    expect(searchService.search).toHaveBeenCalledWith(
      "/repo",
      "hello",
      false,
      ["**/*.ts", "src/**"],
      ["**/*.test.ts"],
    );
  });

  it("clearSearch resets state", () => {
    searchState.query = "foo";
    searchState.results = [{ path: "x", matches: [] }];
    clearSearch();
    expect(searchState.query).toBe("");
    expect(searchState.results).toHaveLength(0);
  });

  it("stores error on failure", async () => {
    const { searchService } = await import("@/api/services");
    (searchService.search as any).mockRejectedValueOnce(new Error("bad regex"));
    await runSearch("/repo", "[invalid");
    expect(searchState.error).toBe("bad regex");
    expect(searchState.loading).toBe(false);
  });

  it("cancels a pending debounced search", async () => {
    vi.useFakeTimers();
    try {
      const { searchService } = await import("@/api/services");
      debouncedSearch("/repo", "later", 100);
      cancelDebouncedSearch();
      await vi.advanceTimersByTimeAsync(100);
      expect(searchService.search).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });

  it("ignores an in-flight result after search cancellation", async () => {
    const { searchService } = await import("@/api/services");
    let resolveSearch!: (value: Awaited<ReturnType<typeof searchService.search>>) => void;
    (searchService.search as ReturnType<typeof vi.fn>).mockImplementationOnce(
      () => new Promise((resolve) => { resolveSearch = resolve; }),
    );

    const pending = runSearch("/repo", "slow");
    cancelDebouncedSearch();
    resolveSearch([{ path: "late.ts", matches: [] }]);
    await pending;

    expect(searchState.results).toEqual([]);
    expect(searchState.loading).toBe(false);
  });

  it("previews replacements without applying them", async () => {
    const { searchService } = await import("@/api/services");
    (searchService.previewReplace as ReturnType<typeof vi.fn>).mockResolvedValue({
      path: "/repo/a.txt",
      originalHash: "hash-a",
      originalContent: "hello",
      modifiedContent: "hi",
      replacements: 1,
    });
    searchState.results = [{ path: "a.txt", matches: [] }];

    await previewReplacements("/repo", "hello", "hi", true);

    expect(searchState.replacePreviews).toEqual([
      expect.objectContaining({ path: "/repo/a.txt", originalHash: "hash-a", selected: true }),
    ]);
    expect(searchService.applyReplacePreview).not.toHaveBeenCalled();
  });

  it("applies selected previews in one backend transaction", async () => {
    const { searchService } = await import("@/api/services");
    (searchService.applyMultiFileReplace as ReturnType<typeof vi.fn>).mockResolvedValue({
      applied: true,
      conflicts: [],
    });
    searchState.replacePreviews = [
      {
        path: "/repo/a.txt",
        originalHash: "hash-a",
        originalContent: "hello",
        modifiedContent: "hi",
        replacements: 1,
        selected: true,
      },
      {
        path: "/repo/b.txt",
        originalHash: "hash-b",
        originalContent: "hello",
        modifiedContent: "hi",
        replacements: 1,
        selected: false,
      },
    ];

    const count = await applySelectedPreviews("hello", "hi", true);

    expect(count).toBe(1);
    expect(searchService.applyMultiFileReplace).toHaveBeenCalledOnce();
    expect(searchService.applyMultiFileReplace).toHaveBeenCalledWith([
      expect.objectContaining({ path: "/repo/a.txt", originalHash: "hash-a" }),
    ]);
    expect(searchService.applyReplacePreview).not.toHaveBeenCalled();
  });

  it("keeps structured conflicts without reporting partial success", async () => {
    const { searchService } = await import("@/api/services");
    (searchService.applyMultiFileReplace as ReturnType<typeof vi.fn>).mockResolvedValue({
      applied: false,
      failureReason: "workspace edit conflict",
      conflicts: ["/repo/b.txt: hash conflict"],
    });
    searchState.replacePreviews = [
      {
        path: "/repo/a.txt",
        originalHash: "hash-a",
        originalContent: "hello",
        modifiedContent: "hi",
        replacements: 1,
        selected: true,
      },
      {
        path: "/repo/b.txt",
        originalHash: "hash-b",
        originalContent: "hello",
        modifiedContent: "hi",
        replacements: 1,
        selected: true,
      },
    ];

    await expect(applySelectedPreviews("hello", "hi", true)).resolves.toBe(0);

    expect(searchState.replaceConflicts).toEqual(["/repo/b.txt: hash conflict"]);
    expect(searchState.error).toContain("workspace edit conflict");
    expect(searchState.error).not.toContain("partial");
    expect(searchState.replacePreviews).toHaveLength(2);
  });

  it("replaceAll previews each file then applies one transaction", async () => {
    const { searchService } = await import("@/api/services");
    searchState.results = [
      { path: "a.txt", matches: [] },
      { path: "b.txt", matches: [] },
    ];
    (searchService.previewReplace as ReturnType<typeof vi.fn>).mockImplementation(
      async (path: string) => ({
        path,
        originalHash: `hash-${path}`,
        originalContent: "hello",
        modifiedContent: "hi",
        replacements: 1,
      }),
    );
    (searchService.applyMultiFileReplace as ReturnType<typeof vi.fn>).mockResolvedValue({
      applied: true,
      conflicts: [],
    });

    await expect(replaceAll("/repo", "hello", "hi", true)).resolves.toBe(2);

    expect(searchService.previewReplace).toHaveBeenCalledTimes(2);
    expect(searchService.applyMultiFileReplace).toHaveBeenCalledOnce();
  });
});
