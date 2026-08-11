import { beforeEach, describe, expect, it, vi } from "vitest";

const searchBindings = vi.hoisted(() => ({
  SearchWithGlobs: vi.fn(),
}));

vi.mock("@wailsio/runtime", () => ({
  Call: { ByID: vi.fn(), ByName: vi.fn() },
  Create: {
    Any: (value: unknown) => value,
    Array: () => (value: unknown) => value,
    Map: () => (value: unknown) => value,
    Nullable: () => (value: unknown) => value,
    Struct: () => (value: unknown) => value,
  },
}));

vi.mock("../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/searchservice.js", () => searchBindings);

import { searchService } from "./search";

describe("search service binding", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("forwards SearchWithGlobs arguments in generated-binding order", async () => {
    searchBindings.SearchWithGlobs.mockResolvedValue([
      {
        filePath: "/workspace/src/main.ts",
        line: 3,
        column: 5,
        preview: "const match = true",
        matches: null,
      },
    ]);

    await expect(searchService.search(
      "/workspace",
      "match",
      true,
      ["src/**/*.ts"],
      ["**/*.test.ts"],
    )).resolves.toEqual([
      {
        filePath: "/workspace/src/main.ts",
        line: 3,
        column: 5,
        preview: "const match = true",
        matches: [],
      },
    ]);
    expect(searchBindings.SearchWithGlobs).toHaveBeenCalledWith(
      "/workspace",
      "match",
      true,
      ["src/**/*.ts"],
      ["**/*.test.ts"],
    );
  });

  it("uses empty glob lists and normalizes a nullable result", async () => {
    searchBindings.SearchWithGlobs.mockResolvedValue(null);

    await expect(
      searchService.search("/workspace", "match", false),
    ).resolves.toEqual([]);
    expect(searchBindings.SearchWithGlobs).toHaveBeenCalledWith(
      "/workspace",
      "match",
      false,
      [],
      [],
    );
  });
});
