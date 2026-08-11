import { describe, expect, it } from "vitest";
import type { LSPDocumentSymbol } from "@/types";
import {
  matchesStructuralFileGlobs,
  parseStructuralPattern,
  searchDocumentSymbols,
} from "./structuralSearch";

function symbol(
  name: string,
  kind: number,
  line: number,
  children: LSPDocumentSymbol[] = [],
): LSPDocumentSymbol {
  return {
    name,
    kind,
    range: {
      start: { line, character: 0 },
      end: { line: line + 3, character: 0 },
    },
    selectionRange: {
      start: { line, character: 2 },
      end: { line, character: 2 + name.length },
    },
    children,
  };
}

describe("structural symbol search", () => {
  const tree = [
    symbol("User", 5, 0, [
      symbol("getName", 6, 1),
      symbol("setName", 6, 5),
    ]),
    symbol("getName", 12, 10),
  ];

  it("matches a kind-qualified ancestor chain and preserves the symbol path", () => {
    const pattern = parseStructuralPattern("class:User > method:get*");
    const results = searchDocumentSymbols("src/user.ts", tree, pattern, true);

    expect(results).toHaveLength(1);
    expect(results[0]).toMatchObject({
      path: "src/user.ts",
      name: "getName",
      kind: 6,
      kindLabel: "method",
      symbolPath: ["User", "getName"],
      selectionRange: {
        start: { line: 1, character: 2 },
        end: { line: 1, character: 9 },
      },
    });
  });

  it("supports case-insensitive glob matching without treating a flat symbol as a child", () => {
    const pattern = parseStructuralPattern("CLASS:user > METHOD:GET*");
    const results = searchDocumentSymbols("src/user.ts", tree, pattern, false);

    expect(results.map((result) => result.name)).toEqual(["getName"]);
  });

  it("rejects empty segments and unknown symbol kinds", () => {
    expect(() => parseStructuralPattern("class:User > ")).toThrow("empty segment");
    expect(() => parseStructuralPattern("widget:User")).toThrow("unknown symbol kind");
  });

  it("applies workspace-relative include and exclude globs", () => {
    expect(matchesStructuralFileGlobs("src/user.ts", ["**/*.ts"], ["**/*.test.ts"])).toBe(true);
    expect(matchesStructuralFileGlobs("src/user.test.ts", ["**/*.ts"], ["**/*.test.ts"])).toBe(false);
    expect(matchesStructuralFileGlobs("README.md", ["**/*.ts"], [])).toBe(false);
  });
});
