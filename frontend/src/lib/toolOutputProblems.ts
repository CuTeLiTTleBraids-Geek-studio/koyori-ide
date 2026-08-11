/**
 * prompt-9 9-J: unified parsers for go test / tsc / eslint-style output → Problems.
 */
// Koyori IDE 模块 · Tool Output Problems。
// 喵，这是 Koyori IDE 的 Tool Output Problems 模块（前端实现）~
export interface ParsedProblem {
  severity: "error" | "warning" | "info" | "hint";
  file: string;
  line: number;
  column: number;
  message: string;
  source: string;
}

/** Parse common compiler/test output lines into problem entries. */
export function parseToolOutputToProblems(output: string, source = "toolchain"): ParsedProblem[] {
  if (!output) return [];
  const out: ParsedProblem[] = [];
  const lines = output.split(/\r?\n/);
  // go test / compiler: file.go:12: message  OR file.go:12:3: message
  const goLine = /^(.+?\.go):(\d+)(?::(\d+))?:\s*(.+)$/;
  // tsc: path(ts): error TSxxxx: msg  OR path:line:col - error
  const tscParen = /^(.+?)\((\d+),(\d+)\):\s*(error|warning)\s+TS\d+:\s*(.+)$/i;
  const tscColon = /^(.+?):(\d+):(\d+)\s*-\s*(error|warning)\s+TS\d+:\s*(.+)$/i;
  // eslint stylish-ish: path:line:col: message
  const generic = /^(.+?):(\d+):(\d+):\s*(.+)$/;
  // vitest / jest FAIL path
  const vitestFail = /FAIL\s+(.+\.(?:ts|tsx|js|jsx))/i;

  for (const raw of lines) {
    const line = raw.trim();
    if (!line) continue;
    let m: RegExpMatchArray | null;
    if ((m = line.match(goLine))) {
      out.push({
        severity: "error",
        file: m[1],
        line: parseInt(m[2], 10) || 1,
        column: parseInt(m[3] || "1", 10) || 1,
        message: m[4],
        source: source.includes("go") ? source : "go",
      });
      continue;
    }
    if ((m = line.match(tscParen))) {
      out.push({
        severity: m[4].toLowerCase() === "warning" ? "warning" : "error",
        file: m[1],
        line: parseInt(m[2], 10) || 1,
        column: parseInt(m[3], 10) || 1,
        message: m[5],
        source: "tsc",
      });
      continue;
    }
    if ((m = line.match(tscColon))) {
      out.push({
        severity: m[4].toLowerCase() === "warning" ? "warning" : "error",
        file: m[1],
        line: parseInt(m[2], 10) || 1,
        column: parseInt(m[3], 10) || 1,
        message: m[5],
        source: "tsc",
      });
      continue;
    }
    if ((m = line.match(vitestFail))) {
      out.push({
        severity: "error",
        file: m[1],
        line: 1,
        column: 1,
        message: line,
        source: "vitest",
      });
      continue;
    }
    if ((m = line.match(generic)) && !line.startsWith("http")) {
      out.push({
        severity: /warn/i.test(m[4]) ? "warning" : "error",
        file: m[1],
        line: parseInt(m[2], 10) || 1,
        column: parseInt(m[3], 10) || 1,
        message: m[4],
        source,
      });
    }
  }
  return out;
}
