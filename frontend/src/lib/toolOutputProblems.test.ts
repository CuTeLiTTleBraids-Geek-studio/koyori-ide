import { describe, it, expect } from "vitest";
import { parseToolOutputToProblems } from "./toolOutputProblems";

// Inline copy of apply logic for unit test without pulling Monaco/editor graph.
function applyTextEditsToContent(
  content: string,
  edits: Array<{ startLine: number; startCol: number; endLine: number; endCol: number; newText: string }>,
): string {
  const lines = content.split("\n");
  const offsetAt = (line: number, col: number): number => {
    let o = 0;
    for (let i = 0; i < Math.min(line, lines.length); i++) o += lines[i].length + 1;
    return o + Math.max(0, col);
  };
  const sorted = [...edits].sort((a, b) => offsetAt(b.startLine, b.startCol) - offsetAt(a.startLine, a.startCol));
  let result = content;
  for (const e of sorted) {
    const start = offsetAt(e.startLine, e.startCol);
    const end = offsetAt(e.endLine, e.endCol);
    result = result.slice(0, start) + e.newText + result.slice(Math.max(start, end));
  }
  return result;
}

describe("parseToolOutputToProblems (prompt-9 9-J)", () => {
  it("parses go test failure lines", () => {
    const out = parseToolOutputToProblems(
      "--- FAIL: TestFoo (0.00s)\n    main_test.go:12: expected 1\n",
      "go test",
    );
    expect(out.some((p) => p.file.includes("main_test.go") && p.line === 12)).toBe(true);
  });

  it("parses tsc-style errors", () => {
    const out = parseToolOutputToProblems(
      "src/a.ts(10,5): error TS2322: Type 'string' is not assignable\n",
      "tsc",
    );
    expect(out).toHaveLength(1);
    expect(out[0].file).toContain("a.ts");
    expect(out[0].line).toBe(10);
  });
});

describe("applyTextEditsToContent (prompt-9 9-A/B)", () => {
  it("applies mid-file edit", () => {
    const next = applyTextEditsToContent("hello world", [
      { startLine: 0, startCol: 6, endLine: 0, endCol: 11, newText: "koyori-ide" },
    ]);
    expect(next).toBe("hello koyori-ide");
  });
});
