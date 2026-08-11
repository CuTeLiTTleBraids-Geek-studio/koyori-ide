/**
 * Tests for G-COMP-03: Auto-import module.
 *
 * Tests the pure logic functions (module detection, import statement
 * construction, insertion position, deduplication) without requiring the
 * backend symbol index service.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import type * as monacoEditor from "monaco-editor";
import type { IndexedSymbol } from "@/types";
import { _internal, getAutoImportCompletions, mergeAutoImportSuggestions } from "@/lib/autoImport";

// Mock the symbolIndexService so getAutoImportCompletions can be tested.
vi.mock("@/api/services", () => ({
  symbolIndexService: {
    getAutoImportCandidates: vi.fn(),
    searchSymbols: vi.fn(),
    getIndexStats: vi.fn(),
    setWorkspaceRoot: vi.fn(),
  },
}));

// Import after mock is set up.
import { symbolIndexService } from "@/api/services";

const {
  detectModuleSystem,
  buildImportStatement,
  findImportInsertPosition,
  isSymbolAlreadyImported,
  buildGoImportEdit,
  findGoImportBinding,
  goPackageQualifier,
  isSamePortableDirectory,
  resolveJSImportPath,
  escapeRegex,
  symbolKindToCompletionKind,
} = _internal;

// ---------------------------------------------------------------------------
// Helper: create an IndexedSymbol
// ---------------------------------------------------------------------------
function makeSymbol(overrides: Partial<IndexedSymbol> = {}): IndexedSymbol {
  return {
    name: "foo",
    kind: 12, // Function
    filePath: "/app/b.js",
    line: 0,
    column: 0,
    exportPath: "./b",
    isDefaultExport: false,
    detail: "function foo() {}",
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// detectModuleSystem
// ---------------------------------------------------------------------------

describe("detectModuleSystem", () => {
  it("detects Go from .go extension", () => {
    expect(detectModuleSystem("/app/main.go", "")).toBe("go");
  });

  it("detects ESM from .ts extension", () => {
    expect(detectModuleSystem("/app/a.ts", "")).toBe("esm");
  });

  it("detects ESM from .tsx extension", () => {
    expect(detectModuleSystem("/app/a.tsx", "")).toBe("esm");
  });

  it("detects ESM from .mjs extension", () => {
    expect(detectModuleSystem("/app/a.mjs", "")).toBe("esm");
  });

  it("detects CJS from .cjs extension", () => {
    expect(detectModuleSystem("/app/a.cjs", "")).toBe("cjs");
  });

  it("detects ESM from .js content with import", () => {
    const content = "import { foo } from './b';\nconsole.log(foo);\n";
    expect(detectModuleSystem("/app/a.js", content)).toBe("esm");
  });

  it("detects ESM from .js content with export", () => {
    const content = "export function foo() {}\n";
    expect(detectModuleSystem("/app/a.js", content)).toBe("esm");
  });

  it("detects CJS from .js content with require", () => {
    const content = "const { foo } = require('./b');\nconsole.log(foo);\n";
    expect(detectModuleSystem("/app/a.js", content)).toBe("cjs");
  });

  it("defaults to ESM for empty .js", () => {
    expect(detectModuleSystem("/app/a.js", "")).toBe("esm");
  });
});

describe("portable import paths", () => {
  it("resolves JS/TS imports relative to the current file directory", () => {
    const symbol = makeSymbol({
      filePath: "/workspace/src/lib/tool.ts",
      exportPath: "./src/lib/tool",
    });
    expect(
      resolveJSImportPath(symbol, "/workspace/src/pages/view.ts"),
    ).toBe("../lib/tool");
  });

  it("handles Windows paths case-insensitively and strips source extensions", () => {
    const symbol = makeSymbol({
      filePath: "C:\\Repo\\src\\lib\\tool.tsx",
      exportPath: "./src/lib/tool",
    });
    expect(resolveJSImportPath(symbol, "c:\\repo\\src\\pages\\view.ts")).toBe(
      "../lib/tool",
    );
    expect(
      isSamePortableDirectory(
        "C:\\Repo\\pkg\\a.go",
        "c:\\repo\\pkg\\b.go",
      ),
    ).toBe(true);
  });

  it("falls back to backend exportPath across unrelated roots", () => {
    const symbol = makeSymbol({
      filePath: "D:\\repo\\lib\\tool.ts",
      exportPath: "./lib/tool",
    });
    expect(resolveJSImportPath(symbol, "C:\\repo\\src\\view.ts")).toBe(
      "./lib/tool",
    );
  });
});

// ---------------------------------------------------------------------------
// buildImportStatement
// ---------------------------------------------------------------------------

describe("buildImportStatement", () => {
  it("builds ESM named import", () => {
    const sym = makeSymbol({ name: "foo", exportPath: "./b", isDefaultExport: false });
    expect(buildImportStatement(sym, "esm", "")).toBe("import { foo } from './b';\n");
  });

  it("builds ESM default import", () => {
    const sym = makeSymbol({ name: "b", exportPath: "./b", isDefaultExport: true });
    expect(buildImportStatement(sym, "esm", "")).toBe("import b from './b';\n");
  });

  it("builds CJS named import", () => {
    const sym = makeSymbol({ name: "foo", exportPath: "./b", isDefaultExport: false });
    expect(buildImportStatement(sym, "cjs", "")).toBe("const { foo } = require('./b');\n");
  });

  it("builds CJS default import", () => {
    const sym = makeSymbol({ name: "b", exportPath: "./b", isDefaultExport: true });
    expect(buildImportStatement(sym, "cjs", "")).toBe("const b = require('./b');\n");
  });

  it("builds Go import path (quoted)", () => {
    const sym = makeSymbol({ name: "Println", exportPath: "fmt", isDefaultExport: false });
    expect(buildImportStatement(sym, "go", "")).toBe('"fmt"');
  });
});

// ---------------------------------------------------------------------------
// findImportInsertPosition
// ---------------------------------------------------------------------------

describe("findImportInsertPosition", () => {
  it("returns position 1:1 for empty file (ESM)", () => {
    const pos = findImportInsertPosition("", "esm");
    expect(pos).toEqual({ line: 1, column: 1 });
  });

  it("returns position after last import (ESM)", () => {
    const content = "import { a } from './a';\nimport { b } from './b';\nconsole.log(a, b);\n";
    const pos = findImportInsertPosition(content, "esm");
    // last import is at line 1 (0-based), so insert at line 3 (1-based)
    expect(pos).toEqual({ line: 3, column: 1 });
  });

  it("inserts after a complete multiline ESM import", () => {
    const content =
      "import {\n  alpha,\n  beta,\n} from './deps';\nconsole.log(alpha, beta);\n";
    expect(findImportInsertPosition(content, "esm")).toEqual({
      line: 5,
      column: 1,
    });
  });

  it("keeps shebang and the full directive prologue before imports", () => {
    const content =
      "#!/usr/bin/env node\n'use server';\n\"use strict\";\nconsole.log('run');\n";
    expect(findImportInsertPosition(content, "esm")).toEqual({
      line: 4,
      column: 1,
    });
  });

  it("keeps TypeScript reference and check comments before a new import", () => {
    const content =
      '/// <reference types="vite/client" />\n// @ts-check\nconst ready = true;\n';
    expect(findImportInsertPosition(content, "esm")).toEqual({
      line: 3,
      column: 1,
    });
  });

  it("does not separate a declaration from its comment after imports", () => {
    const content =
      "import { boot } from './boot';\n// Starts the application.\nconst app = boot();\n";
    expect(findImportInsertPosition(content, "esm")).toEqual({
      line: 2,
      column: 1,
    });
  });

  it("returns position after use strict (ESM)", () => {
    const content = "'use strict';\nconsole.log(1);\n";
    const pos = findImportInsertPosition(content, "esm");
    expect(pos).toEqual({ line: 2, column: 1 });
  });

  it("returns position 1:1 when no imports (ESM)", () => {
    const content = "console.log('hello');\n";
    const pos = findImportInsertPosition(content, "esm");
    expect(pos).toEqual({ line: 1, column: 1 });
  });

  it("returns position after CJS require (CJS)", () => {
    const content = "const { a } = require('./a');\nconsole.log(a);\n";
    const pos = findImportInsertPosition(content, "cjs");
    expect(pos).toEqual({ line: 2, column: 1 });
  });

  it("ignores require calls after the top-level import region", () => {
    const content =
      "function load() {\n  return require('./lazy');\n}\nconsole.log(load);\n";
    expect(findImportInsertPosition(content, "cjs")).toEqual({
      line: 1,
      column: 1,
    });
  });

  it("returns position inside Go import block", () => {
    const content = "package main\n\nimport (\n\t\"fmt\"\n)\n\nfunc main() {}\n";
    const pos = findImportInsertPosition(content, "go");
    // block ends at line 5 (0-based index 4, 1-based line 5)
    // findImportInsertPosition returns blockEnd + 1 = 5 (1-based)
    expect(pos.line).toBe(5);
    expect(pos.column).toBe(1);
  });

  it("returns position after package for Go without imports", () => {
    const content = "package main\n\nfunc main() {}\n";
    const pos = findImportInsertPosition(content, "go");
    // package at line 0, insert at line 2 (1-based)
    expect(pos).toEqual({ line: 2, column: 1 });
  });
});

// ---------------------------------------------------------------------------
// buildGoImportEdit
// ---------------------------------------------------------------------------

describe("buildGoImportEdit", () => {
  it("inserts into existing import block", () => {
    const content = "package main\n\nimport (\n\t\"fmt\"\n)\n\nfunc main() {}\n";
    const sym = makeSymbol({ name: "Println", exportPath: "strings" });
    const result = buildGoImportEdit(sym, content);
    expect(result.line).toBe(4); // line index of ")" (0-based)
    expect(result.text).toBe('\t"strings"\n');
  });

  it("returns empty when already imported", () => {
    const content = "package main\n\nimport (\n\t\"fmt\"\n)\n\nfunc main() {}\n";
    const sym = makeSymbol({ name: "Println", exportPath: "fmt" });
    const result = buildGoImportEdit(sym, content);
    expect(result.line).toBe(-1);
    expect(result.text).toBe("");
  });

  it("does not treat a quoted import path in a comment as an import", () => {
    const content =
      'package main\n\n// use "fmt" when diagnostics are enabled\nimport (\n\t"strings"\n)\n';
    const sym = makeSymbol({ name: "Println", exportPath: "fmt" });
    const result = buildGoImportEdit(sym, content, "fmt");
    expect(result.line).toBe(5);
    expect(result.text).toBe('\tfmt "fmt"\n');
  });

  it("creates new import block after package", () => {
    const content = "package main\n\nfunc main() {}\n";
    const sym = makeSymbol({ name: "Println", exportPath: "fmt" });
    const result = buildGoImportEdit(sym, content);
    expect(result.line).toBe(1); // after package line (0-based)
    expect(result.text).toContain("import (");
    expect(result.text).toContain('"fmt"');
  });

  it("uses an explicit alias when requested", () => {
    const content = "package main\n\nfunc main() {}\n";
    const sym = makeSymbol({
      name: "Open",
      exportPath: "example.com/acme/my-lib/v2",
    });
    const result = buildGoImportEdit(sym, content, "my_lib");
    expect(result.text).toContain(
      'my_lib "example.com/acme/my-lib/v2"',
    );
  });

  it("derives legal aliases and ignores semantic version suffixes", () => {
    expect(goPackageQualifier("example.com/acme/my-lib/v2")).toBe("my_lib");
    expect(goPackageQualifier("gopkg.in/yaml.v3")).toBe("yaml");
    expect(goPackageQualifier("example.com/acme/type")).toBe("type_pkg");
    expect(goPackageQualifier("example.com/acme/123pkg")).toBe("_123pkg");
  });

  it("reads explicit and unaliased Go import bindings", () => {
    const content =
      'package main\n\nimport (\n\talias "example.com/acme/lib"\n\t"fmt"\n)\n';
    expect(findGoImportBinding(content, "example.com/acme/lib")).toEqual({
      alias: "alias",
    });
    expect(findGoImportBinding(content, "fmt")).toEqual({ alias: null });
  });
});

// ---------------------------------------------------------------------------
// isSymbolAlreadyImported
// ---------------------------------------------------------------------------

describe("isSymbolAlreadyImported", () => {
  it("detects existing ESM named import", () => {
    const content = "import { foo } from './b';\nconsole.log(foo);\n";
    const sym = makeSymbol({ name: "foo", exportPath: "./b", isDefaultExport: false });
    expect(isSymbolAlreadyImported(sym, content, "esm")).toBe(true);
  });

  it("detects existing ESM default import", () => {
    const content = "import b from './b';\nconsole.log(b);\n";
    const sym = makeSymbol({ name: "b", exportPath: "./b", isDefaultExport: true });
    expect(isSymbolAlreadyImported(sym, content, "esm")).toBe(true);
  });

  it("detects existing CJS named import", () => {
    const content = "const { foo } = require('./b');\nconsole.log(foo);\n";
    const sym = makeSymbol({ name: "foo", exportPath: "./b", isDefaultExport: false });
    expect(isSymbolAlreadyImported(sym, content, "cjs")).toBe(true);
  });

  it("detects an existing CJS default require binding", () => {
    const content = "const client = require( './client' );\nclient.run();\n";
    const sym = makeSymbol({
      name: "client",
      exportPath: "./client",
      isDefaultExport: true,
    });
    expect(isSymbolAlreadyImported(sym, content, "cjs")).toBe(true);
  });

  it("detects existing Go import", () => {
    const content = 'package main\n\nimport (\n\t"fmt"\n)\n\nfunc main() {}\n';
    const sym = makeSymbol({ name: "Println", exportPath: "fmt" });
    expect(isSymbolAlreadyImported(sym, content, "go")).toBe(true);
  });

  it("returns false for non-imported symbol (ESM)", () => {
    const content = "console.log('hello');\n";
    const sym = makeSymbol({ name: "foo", exportPath: "./b", isDefaultExport: false });
    expect(isSymbolAlreadyImported(sym, content, "esm")).toBe(false);
  });

  it("returns false when module imported but symbol not (ESM named)", () => {
    const content = "import { bar } from './b';\nconsole.log(bar);\n";
    const sym = makeSymbol({ name: "foo", exportPath: "./b", isDefaultExport: false });
    expect(isSymbolAlreadyImported(sym, content, "esm")).toBe(false);
  });

  it("handles double-quote imports", () => {
    const content = 'import { foo } from "./b";\nconsole.log(foo);\n';
    const sym = makeSymbol({ name: "foo", exportPath: "./b", isDefaultExport: false });
    expect(isSymbolAlreadyImported(sym, content, "esm")).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// escapeRegex
// ---------------------------------------------------------------------------

describe("escapeRegex", () => {
  it("escapes special regex characters", () => {
    expect(escapeRegex("./path/file")).toBe("\\./path/file");
    expect(escapeRegex("a+b*c")).toBe("a\\+b\\*c");
    expect(escapeRegex("[test]")).toBe("\\[test\\]");
  });
});

describe("symbolKindToCompletionKind", () => {
  it.each([
    ["File", 1, 20],
    ["Module", 2, 8],
    ["Namespace", 3, 8],
    ["Package", 4, 8],
    ["Class", 5, 5],
    ["Method", 6, 0],
    ["Property", 7, 9],
    ["Field", 8, 3],
    ["Constructor", 9, 2],
    ["Enum", 10, 15],
    ["Interface", 11, 7],
    ["Function", 12, 1],
    ["Variable", 13, 4],
    ["Constant", 14, 14],
    ["String", 15, 13],
    ["Number", 16, 13],
    ["Boolean", 17, 13],
    ["Array", 18, 13],
    ["Object", 19, 6],
    ["Key", 20, 9],
    ["Null", 21, 13],
    ["EnumMember", 22, 16],
    ["Struct", 23, 6],
    ["Event", 24, 10],
    ["Operator", 25, 11],
    ["TypeParameter", 26, 24],
  ])("maps LSP %s (%i) to Monaco CompletionItemKind %i", (_name, lspKind, monacoKind) => {
    expect(symbolKindToCompletionKind(lspKind)).toBe(monacoKind);
  });

  it("falls back to Variable for an unknown SymbolKind", () => {
    expect(symbolKindToCompletionKind(0)).toBe(4);
    expect(symbolKindToCompletionKind(99)).toBe(4);
  });
});

// ---------------------------------------------------------------------------
// getAutoImportCompletions (integration with mocked symbol index)
// ---------------------------------------------------------------------------

describe("getAutoImportCompletions", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("returns empty for empty typed word", async () => {
    const result = await getAutoImportCompletions("", "/app/a.js", "console.log(1);", "javascript");
    expect(result).toEqual([]);
  });

  it("returns empty for unsupported language", async () => {
    const result = await getAutoImportCompletions("foo", "/app/a.py", "print(1)", "python");
    expect(result).toEqual([]);
  });

  it("returns ESM auto-import suggestion for matching symbol", async () => {
    const sym = makeSymbol({ name: "foo", exportPath: "./b", isDefaultExport: false });
    vi.mocked(symbolIndexService.getAutoImportCandidates).mockResolvedValue([sym]);

    const result = await getAutoImportCompletions(
      "foo",
      "/app/a.js",
      "console.log('hello');\n",
      "javascript",
    );

    expect(result.length).toBe(1);
    expect(result[0].label).toBe("foo");
    expect(result[0].insertText).toBe("foo");
    expect(result[0].additionalTextEdits[0].text).toBe("import { foo } from './b';\n");
  });

  it("uses the current file directory for nested JS/TS imports", async () => {
    const sym = makeSymbol({
      name: "tool",
      filePath: "/workspace/src/lib/tool.ts",
      exportPath: "./src/lib/tool",
    });
    vi.mocked(symbolIndexService.getAutoImportCandidates).mockResolvedValue([
      sym,
    ]);

    const result = await getAutoImportCompletions(
      "tool",
      "/workspace/src/pages/view.ts",
      "tool();\n",
      "typescript",
    );

    expect(result).toHaveLength(1);
    expect(result[0].detail).toBe("auto-import from ../lib/tool");
    expect(result[0].additionalTextEdits[0].text).toBe(
      "import { tool } from '../lib/tool';\n",
    );
  });

  it("deduplicates against an already imported resolved relative path", async () => {
    const sym = makeSymbol({
      name: "tool",
      filePath: "/workspace/src/lib/tool.ts",
      exportPath: "./src/lib/tool",
    });
    vi.mocked(symbolIndexService.getAutoImportCandidates).mockResolvedValue([
      sym,
    ]);
    const content = "import { tool } from '../lib/tool';\ntool();\n";

    await expect(
      getAutoImportCompletions(
        "tool",
        "/workspace/src/pages/view.ts",
        content,
        "typescript",
      ),
    ).resolves.toEqual([]);
  });

  it("returns ESM default import suggestion", async () => {
    const sym = makeSymbol({ name: "b", exportPath: "./b", isDefaultExport: true });
    vi.mocked(symbolIndexService.getAutoImportCandidates).mockResolvedValue([sym]);

    const result = await getAutoImportCompletions(
      "b",
      "/app/a.js",
      "console.log('hello');\n",
      "javascript",
    );

    expect(result.length).toBe(1);
    expect(result[0].label).toBe("b");
    expect(result[0].additionalTextEdits[0].text).toBe("import b from './b';\n");
  });

  it("returns CJS import for CommonJS file", async () => {
    const sym = makeSymbol({ name: "foo", exportPath: "./b", isDefaultExport: false });
    vi.mocked(symbolIndexService.getAutoImportCandidates).mockResolvedValue([sym]);

    const result = await getAutoImportCompletions(
      "foo",
      "/app/a.cjs",
      "const x = require('./c');\n",
      "javascript",
    );

    expect(result.length).toBe(1);
    expect(result[0].additionalTextEdits[0].text).toBe("const { foo } = require('./b');\n");
  });

  it("returns Go import for .go file", async () => {
    // 符号必须来自 .go 文件（跨语言隔离：Go 文件只匹配 Go 符号）。
    const sym = makeSymbol({ name: "Println", exportPath: "fmt", isDefaultExport: false, filePath: "/workspace/fmt.go" });
    vi.mocked(symbolIndexService.getAutoImportCandidates).mockResolvedValue([sym]);

    const result = await getAutoImportCompletions(
      "Println",
      "/app/main.go",
      "package main\n\nfunc main() {}\n",
      "go",
    );

    expect(result.length).toBe(1);
    expect(result[0].label).toBe("Println");
    expect(result[0].insertText).toBe("fmt.Println");
    expect(result[0].additionalTextEdits[0].text).toContain('fmt "fmt"');
  });

  it("keeps a qualified Go suggestion when the package is already imported", async () => {
    const sym = makeSymbol({
      name: "Println",
      exportPath: "fmt",
      filePath: "/workspace/fmt/print.go",
    });
    vi.mocked(symbolIndexService.getAutoImportCandidates).mockResolvedValue([
      sym,
    ]);
    const content = 'package main\n\nimport "fmt"\n\nfunc main() {}\n';

    const result = await getAutoImportCompletions(
      "Println",
      "/workspace/cmd/main.go",
      content,
      "go",
    );

    expect(result).toHaveLength(1);
    expect(result[0].insertText).toBe("fmt.Println");
    expect(result[0].additionalTextEdits).toEqual([]);
  });

  it("preserves an existing explicit Go import alias", async () => {
    const sym = makeSymbol({
      name: "Open",
      exportPath: "example.com/acme/my-lib/v2",
      filePath: "/workspace/my-lib/open.go",
    });
    vi.mocked(symbolIndexService.getAutoImportCandidates).mockResolvedValue([
      sym,
    ]);
    const content =
      'package main\n\nimport lib "example.com/acme/my-lib/v2"\n\nfunc main() {}\n';

    const result = await getAutoImportCompletions(
      "Open",
      "/workspace/cmd/main.go",
      content,
      "go",
    );

    expect(result).toHaveLength(1);
    expect(result[0].insertText).toBe("lib.Open");
    expect(result[0].additionalTextEdits).toEqual([]);
  });

  it("skips Go symbols from the same package directory", async () => {
    const sym = makeSymbol({
      name: "Shared",
      exportPath: "example.com/acme/pkg",
      filePath: "/workspace/pkg/shared.go",
    });
    vi.mocked(symbolIndexService.getAutoImportCandidates).mockResolvedValue([
      sym,
    ]);

    await expect(
      getAutoImportCompletions(
        "Shared",
        "/workspace/pkg/current.go",
        "package pkg\n",
        "go",
      ),
    ).resolves.toEqual([]);
  });

  it("skips Go methods because they are not package-level selectors", async () => {
    const sym = makeSymbol({
      name: "Serve",
      kind: 6,
      exportPath: "example.com/acme/server",
      filePath: "/workspace/server/server.go",
    });
    vi.mocked(symbolIndexService.getAutoImportCandidates).mockResolvedValue([
      sym,
    ]);

    await expect(
      getAutoImportCompletions(
        "Serve",
        "/workspace/cmd/main.go",
        "package main\n",
        "go",
      ),
    ).resolves.toEqual([]);
  });

  it("skips already-imported symbols", async () => {
    const sym = makeSymbol({ name: "foo", exportPath: "./b", isDefaultExport: false });
    vi.mocked(symbolIndexService.getAutoImportCandidates).mockResolvedValue([sym]);

    const content = "import { foo } from './b';\nconsole.log(foo);\n";
    const result = await getAutoImportCompletions("foo", "/app/a.js", content, "javascript");
    expect(result).toEqual([]);
  });

  it("跨语言隔离：Go 文件不返回 JS/TS 符号（对标 VSCode）", async () => {
    // 后端 symbol index 混合了所有语言的符号，前端必须按语言过滤。
    const jsSym = makeSymbol({ name: "foo", exportPath: "./b", filePath: "/workspace/b.js" });
    const goSym = makeSymbol({ name: "Bar", exportPath: "pkg/bar", filePath: "/workspace/bar.go" });
    vi.mocked(symbolIndexService.getAutoImportCandidates).mockResolvedValue([jsSym, goSym]);

    const result = await getAutoImportCompletions(
      "f",
      "/app/main.go",
      "package main\n\nfunc main() {}\n",
      "go",
    );
    // 只返回 Go 符号，不返回 JS 符号
    expect(result.length).toBe(1);
    expect(result[0].label).toBe("Bar");
  });

  it("跨语言隔离：JS 文件不返回 Go 符号（对标 VSCode）", async () => {
    const jsSym = makeSymbol({ name: "foo", exportPath: "./b", filePath: "/workspace/b.js" });
    const goSym = makeSymbol({ name: "Bar", exportPath: "pkg/bar", filePath: "/workspace/bar.go" });
    vi.mocked(symbolIndexService.getAutoImportCandidates).mockResolvedValue([jsSym, goSym]);

    const result = await getAutoImportCompletions(
      "f",
      "/app/a.js",
      "console.log(1);\n",
      "javascript",
    );
    // 只返回 JS 符号，不返回 Go 符号
    expect(result.length).toBe(1);
    expect(result[0].label).toBe("foo");
  });

  it("跨语言隔离：TypeScript 文件不返回 Go 符号", async () => {
    const tsSym = makeSymbol({
      name: "foo",
      exportPath: "./b",
      filePath: "/workspace/b.ts",
    });
    const goSym = makeSymbol({
      name: "Bar",
      exportPath: "pkg/bar",
      filePath: "/workspace/bar.go",
    });
    vi.mocked(symbolIndexService.getAutoImportCandidates).mockResolvedValue([
      tsSym,
      goSym,
    ]);

    const result = await getAutoImportCompletions(
      "f",
      "/app/a.ts",
      "const value = 1;\n",
      "typescript",
    );

    expect(result).toHaveLength(1);
    expect(result[0].label).toBe("foo");
  });

  it("handles backend error gracefully", async () => {
    vi.mocked(symbolIndexService.getAutoImportCandidates).mockRejectedValue(new Error("rpc"));

    const result = await getAutoImportCompletions(
      "foo",
      "/app/a.js",
      "console.log(1);",
      "javascript",
    );
    expect(result).toEqual([]);
  });

  it("returns multiple suggestions for multiple matches", async () => {
    const sym1 = makeSymbol({ name: "foo", exportPath: "./b", isDefaultExport: false });
    const sym2 = makeSymbol({
      name: "foo",
      exportPath: "./c",
      isDefaultExport: false,
      filePath: "/app/c.js",
    });
    vi.mocked(symbolIndexService.getAutoImportCandidates).mockResolvedValue([sym1, sym2]);

    const result = await getAutoImportCompletions(
      "foo",
      "/app/a.js",
      "console.log(1);",
      "javascript",
    );

    expect(result.length).toBe(2);
    expect(result[0].additionalTextEdits[0].text).toContain("./b");
    expect(result[1].additionalTextEdits[0].text).toContain("./c");
  });

  it("places import after existing imports (ESM)", async () => {
    const sym = makeSymbol({ name: "foo", exportPath: "./b", isDefaultExport: false });
    vi.mocked(symbolIndexService.getAutoImportCandidates).mockResolvedValue([sym]);

    const content = "import { bar } from './a';\nconsole.log(bar);\n";
    const result = await getAutoImportCompletions("foo", "/app/a.js", content, "javascript");

    expect(result.length).toBe(1);
    // Insert position should be after the existing import (line 2, 1-based)
    expect(result[0].additionalTextEdits[0].range.startLineNumber).toBe(2);
  });
});

// ---------------------------------------------------------------------------
// mergeAutoImportSuggestions
// ---------------------------------------------------------------------------

describe("mergeAutoImportSuggestions", () => {
  it("merges auto-import into empty LSP list", () => {
    const range = { startLineNumber: 1, endLineNumber: 1, startColumn: 1, endColumn: 5 };
    const autoImports = [
      {
        label: "foo",
        kind: 3,
        detail: "auto-import from ./b",
        insertText: "foo",
        sortText: "0",
        additionalTextEdits: [
          {
            range: { startLineNumber: 1, startColumn: 1, endLineNumber: 1, endColumn: 1 },
            text: "import { foo } from './b';\n",
          },
        ],
      },
    ];

    const merged = mergeAutoImportSuggestions([], autoImports, range);
    expect(merged.length).toBe(1);
    expect(merged[0].label).toBe("foo");
  });

  it("skips auto-import when LSP already has same label", () => {
    const range = { startLineNumber: 1, endLineNumber: 1, startColumn: 1, endColumn: 5 };
    const lspSuggestions: monacoEditor.languages.CompletionItem[] = [
      {
        label: "foo",
        kind: 3,
        insertText: "foo",
        range,
      },
    ];
    const autoImports = [
      {
        label: "foo",
        kind: 3,
        detail: "auto-import from ./b",
        insertText: "foo",
        sortText: "0",
        additionalTextEdits: [
          {
            range: { startLineNumber: 1, startColumn: 1, endLineNumber: 1, endColumn: 1 },
            text: "import { foo } from './b';\n",
          },
        ],
      },
      {
        label: "foo",
        kind: 3,
        detail: "auto-import from ./c",
        insertText: "foo",
        sortText: "0",
        additionalTextEdits: [
          {
            range: {
              startLineNumber: 1,
              startColumn: 1,
              endLineNumber: 1,
              endColumn: 1,
            },
            text: "import { foo } from './c';\n",
          },
        ],
      },
    ];

    const merged = mergeAutoImportSuggestions(lspSuggestions, autoImports, range);
    expect(merged.length).toBe(1);
  });

  it("merges non-duplicate labels", () => {
    const range = { startLineNumber: 1, endLineNumber: 1, startColumn: 1, endColumn: 5 };
    const lspSuggestions: monacoEditor.languages.CompletionItem[] = [
      { label: "bar", kind: 3, insertText: "bar", range },
    ];
    const autoImports = [
      {
        label: "foo",
        kind: 3,
        detail: "auto-import from ./b",
        insertText: "foo",
        sortText: "0",
        additionalTextEdits: [
          {
            range: { startLineNumber: 1, startColumn: 1, endLineNumber: 1, endColumn: 1 },
            text: "import { foo } from './b';\n",
          },
        ],
      },
    ];

    const merged = mergeAutoImportSuggestions(lspSuggestions, autoImports, range);
    expect(merged.length).toBe(2);
    expect(merged[0].label).toBe("bar");
    expect(merged[1].label).toBe("foo");
  });

  it("keeps same-name auto-imports from different module sources", () => {
    const range = {
      startLineNumber: 1,
      endLineNumber: 1,
      startColumn: 1,
      endColumn: 5,
    };
    const autoImports = [
      {
        label: "foo",
        kind: 3,
        detail: "auto-import from ./b",
        insertText: "foo",
        sortText: "0",
        additionalTextEdits: [],
      },
      {
        label: "foo",
        kind: 3,
        detail: "auto-import from ./c",
        insertText: "foo",
        sortText: "0",
        additionalTextEdits: [],
      },
    ];

    const merged = mergeAutoImportSuggestions([], autoImports, range);
    expect(merged).toHaveLength(2);
    expect(merged.map((item) => item.detail)).toEqual([
      "auto-import from ./b",
      "auto-import from ./c",
    ]);
  });
});
