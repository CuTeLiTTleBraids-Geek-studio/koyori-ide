import { beforeEach, describe, expect, it, vi } from "vitest";
import type { LSPDocumentSymbol } from "@/types";

const api = vi.hoisted(() => ({
  listAllFiles: vi.fn(),
  readFile: vi.fn(),
  previewStructuralReplace: vi.fn(),
  applyStructuralReplacePreview: vi.fn(),
}));

const lsp = vi.hoisted(() => ({
  ensureRunning: vi.fn(),
  getDocumentSymbols: vi.fn(),
}));

vi.mock("@/api/services", () => ({
  fileService: {
    listAllFiles: api.listAllFiles,
    readFile: api.readFile,
  },
  searchService: {
    previewStructuralReplace: api.previewStructuralReplace,
    applyStructuralReplacePreview: api.applyStructuralReplacePreview,
  },
}));

vi.mock("@/stores/lsp", () => ({
  ensureLSPRunning: lsp.ensureRunning,
  getLSPDocumentSymbols: lsp.getDocumentSymbols,
  monacoLanguageToLSP: (language: string) => (
    ["typescript", "javascript", "go", "json", "css", "html", "yaml"].includes(language)
      ? language
      : null
  ),
}));

vi.mock("@/lib/language", () => ({
  detectLanguage: (path: string) => {
    if (path.endsWith(".ts")) return "typescript";
    if (path.endsWith(".go")) return "go";
    return "plaintext";
  },
}));

import {
  applySelectedStructuralPreviews,
  cancelStructuralSearch,
  clearStructuralSearch,
  previewStructuralReplacements,
  runStructuralSearch,
  structuralSearchState,
} from "./structuralSearch";

function symbol(name: string, kind: number, line: number, children: LSPDocumentSymbol[] = []): LSPDocumentSymbol {
  return {
    name,
    kind,
    range: { start: { line, character: 0 }, end: { line: line + 2, character: 0 } },
    selectionRange: {
      start: { line, character: 2 },
      end: { line, character: 2 + name.length },
    },
    children,
  };
}

describe("structural search store", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    clearStructuralSearch();
    api.listAllFiles.mockResolvedValue(["src/user.ts", "src/user.test.ts", "README.md"]);
    api.readFile.mockResolvedValue("class User {}\n");
    lsp.ensureRunning.mockResolvedValue(true);
    lsp.getDocumentSymbols.mockResolvedValue([
      symbol("User", 5, 0, [symbol("getName", 6, 1)]),
    ]);
  });

  it("starts each language once and skips files whose LSP is unavailable", async () => {
    api.listAllFiles.mockResolvedValue(["src/user.ts", "cmd/main.go"]);
    lsp.ensureRunning.mockImplementation(async (language: string) => language === "typescript");

    await runStructuralSearch("/repo", "class:User", { caseSensitive: true });

    expect(lsp.ensureRunning).toHaveBeenCalledTimes(2);
    expect(lsp.ensureRunning).toHaveBeenCalledWith("typescript");
    expect(lsp.ensureRunning).toHaveBeenCalledWith("go");
    expect(api.readFile).toHaveBeenCalledTimes(1);
    expect(api.readFile).toHaveBeenCalledWith("/repo/src/user.ts");
    expect(structuralSearchState.skippedFiles).toBe(1);
  });

  it("searches supported workspace files and honors include/exclude globs", async () => {
    await runStructuralSearch("/repo", "class:User > method:get*", {
      caseSensitive: true,
      includeGlobs: ["**/*.ts"],
      excludeGlobs: ["**/*.test.ts"],
    });

    expect(api.readFile).toHaveBeenCalledTimes(1);
    expect(api.readFile).toHaveBeenCalledWith("/repo/src/user.ts");
    expect(lsp.getDocumentSymbols).toHaveBeenCalledWith(
      "typescript",
      "/repo/src/user.ts",
      "class User {}\n",
    );
    expect(structuralSearchState.results).toEqual([
      expect.objectContaining({
        path: "src/user.ts",
        name: "getName",
        symbolPath: ["User", "getName"],
        selected: true,
      }),
    ]);
    expect(structuralSearchState.loading).toBe(false);
  });

  it("ignores results from a cancelled in-flight symbol request", async () => {
    api.listAllFiles.mockResolvedValue(["src/user.ts"]);
    let resolveSymbols!: (value: LSPDocumentSymbol[]) => void;
    lsp.getDocumentSymbols.mockImplementationOnce(
      () => new Promise<LSPDocumentSymbol[]>((resolve) => { resolveSymbols = resolve; }),
    );

    const pending = runStructuralSearch("/repo", "method:get*", { caseSensitive: true });
    await vi.waitFor(() => expect(lsp.getDocumentSymbols).toHaveBeenCalledTimes(1));
    cancelStructuralSearch();
    resolveSymbols([symbol("getLate", 6, 1)]);
    await pending;

    expect(structuralSearchState.results).toEqual([]);
    expect(structuralSearchState.loading).toBe(false);
  });

  it("groups selected symbol ranges into baseline-protected file previews", async () => {
    structuralSearchState.results = [
      {
        path: "src/user.ts",
        name: "getName",
        kind: 6,
        kindLabel: "method",
        symbolPath: ["User", "getName"],
        selectionRange: { start: { line: 1, character: 2 }, end: { line: 1, character: 9 } },
        selected: true,
      },
      {
        path: "src/user.ts",
        name: "setName",
        kind: 6,
        kindLabel: "method",
        symbolPath: ["User", "setName"],
        selectionRange: { start: { line: 3, character: 2 }, end: { line: 3, character: 9 } },
        selected: false,
      },
    ];
    api.previewStructuralReplace.mockResolvedValue({
      path: "/repo/src/user.ts",
      originalHash: "hash-a",
      originalContent: "getName",
      modifiedContent: "displayName",
      replacements: 1,
    });
    api.applyStructuralReplacePreview.mockResolvedValue({ replacements: 1 });

    await previewStructuralReplacements("/repo", "displayName");

    expect(api.previewStructuralReplace).toHaveBeenCalledWith("/repo/src/user.ts", [{
      startLine: 1,
      startCharacter: 2,
      endLine: 1,
      endCharacter: 9,
      expectedText: "getName",
      replacement: "displayName",
    }]);
    expect(structuralSearchState.previews).toEqual([
      expect.objectContaining({ originalHash: "hash-a", selected: true, edits: expect.any(Array) }),
    ]);

    const applied = await applySelectedStructuralPreviews();
    expect(applied).toBe(1);
    expect(api.applyStructuralReplacePreview).toHaveBeenCalledWith(
      "/repo/src/user.ts",
      "hash-a",
      expect.any(Array),
    );
  });
});
