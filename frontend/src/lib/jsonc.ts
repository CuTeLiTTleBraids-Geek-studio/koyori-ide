// Koyori IDE 模块 · Jsonc。
// 喵，这是 Koyori IDE 的 Jsonc 模块（前端实现）~
function stripJSONComments(source: string): string {
  let output = "";
  let inString = false;
  let escaped = false;
  let lineComment = false;
  let blockComment = false;

  for (let index = 0; index < source.length; index += 1) {
    const char = source[index];
    const next = source[index + 1];

    if (lineComment) {
      if (char === "\n" || char === "\r") {
        lineComment = false;
        output += char;
      } else {
        output += " ";
      }
      continue;
    }

    if (blockComment) {
      if (char === "*" && next === "/") {
        output += "  ";
        index += 1;
        blockComment = false;
      } else {
        output += char === "\n" || char === "\r" ? char : " ";
      }
      continue;
    }

    if (inString) {
      output += char;
      if (escaped) {
        escaped = false;
      } else if (char === "\\") {
        escaped = true;
      } else if (char === '"') {
        inString = false;
      }
      continue;
    }

    if (char === '"') {
      inString = true;
      output += char;
    } else if (char === "/" && next === "/") {
      output += "  ";
      index += 1;
      lineComment = true;
    } else if (char === "/" && next === "*") {
      output += "  ";
      index += 1;
      blockComment = true;
    } else {
      output += char;
    }
  }

  if (blockComment) throw new SyntaxError("Unterminated JSONC block comment");
  return output;
}

function stripTrailingCommas(source: string): string {
  let output = "";
  let inString = false;
  let escaped = false;

  for (let index = 0; index < source.length; index += 1) {
    const char = source[index];
    if (inString) {
      output += char;
      if (escaped) {
        escaped = false;
      } else if (char === "\\") {
        escaped = true;
      } else if (char === '"') {
        inString = false;
      }
      continue;
    }

    if (char === '"') {
      inString = true;
      output += char;
      continue;
    }

    if (char === ",") {
      let nextIndex = index + 1;
      while (/\s/.test(source[nextIndex] ?? "")) nextIndex += 1;
      if (source[nextIndex] === "}" || source[nextIndex] === "]") continue;
    }
    output += char;
  }
  return output;
}

export function parseJSONC<T>(source: string): T {
  return JSON.parse(stripTrailingCommas(stripJSONComments(source))) as T;
}
