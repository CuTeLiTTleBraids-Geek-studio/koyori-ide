#!/usr/bin/env node

// Personal-path leak guard (P19 P1-05 / P20 P1-01): fails when any committable
// text file embeds a personal user profile path in any of these forms:
//   - Windows: `C:\Users\<name>\...` and its escaped doubling `C:\\Users\\<name>`
//   - WSL mount: `/mnt/c/Users/<name>/...`
//   - Linux home: `/home/<name>/...`
// Scope follows the P19 acceptance rule "已跟踪文件中个人路径为 0": the file
// set is `git ls-files --cached --others --exclude-standard`, i.e. tracked
// files plus untracked-but-not-ignored files, so local ignored artifacts
// (e.g. the gitignored build/overlay_windows.json) never produce noise.
// The docs/prompts/ archive is deliberately NOT excluded (unlike
// check-encoding.mjs) because prompt evidence text is exactly where local
// paths historically leaked. `%USERPROFILE%` style placeholders and the
// generic `C:\Users\<具体用户名>` pattern are not flagged; a small allowlist
// covers non-identifying fixture names used by existing tests.

import { execFile } from "node:child_process";
import { readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const textExtensions = new Set([
  ".css", ".go", ".html", ".js", ".json", ".md", ".mjs", ".plist", ".ps1",
  ".sh", ".svg", ".ts", ".tsx", ".txt", ".vue", ".xml", ".yaml", ".yml",
]);
const textBasenames = new Set(["LICENSE", "NOTICE", "VERSION", ".gitignore"]);

// Segment names that are generic test fixtures or documentation placeholders,
// never real usernames.
const allowedSegments = new Set([
  "example", "dev", "public", "default", "main.go", "proj",
  "user", "alice",
]);

// Path segment: excludes separators, quoting, placeholders (`<`, `%`) and
// wildcard characters, so `C:\Users\<具体用户名>` and `/home/<user>` never
// match. `{1,2}` on the separators accepts the escaped doubling form (two
// backslashes, as markdown/code spans previously emitted) that docs used.
const personalPathPatterns = [
  /c:[\\/]{1,2}users[\\/]{1,2}([^\\/<>"'`%|:*?\s]+)/gi, // Windows profile (raw + escaped)
  /\/mnt\/c\/users\/([^\\/<>"'`%|:*?\s]+)/gi, // WSL-mounted Windows profile
  /\/home\/([^\\/<>"'`%|:*?\s]+)/gi, // Linux home directory
];

export function findPersonalPaths(text) {
  const findings = [];
  for (const pattern of personalPathPatterns) {
    for (const match of text.matchAll(pattern)) {
      const segment = match[1];
      if (allowedSegments.has(segment.toLowerCase())) continue;
      findings.push({ match: match[0], index: match.index });
    }
  }
  return findings;
}

function shouldCheck(repoRelativePath) {
  if (repoRelativePath.startsWith("build/e2e-evidence/")) return false;
  const name = path.basename(repoRelativePath);
  return textBasenames.has(name) || textExtensions.has(path.extname(name).toLowerCase());
}

async function listCommittableFiles() {
  const { stdout } = await execFileAsync(
    "git",
    ["ls-files", "--cached", "--others", "--exclude-standard", "-z"],
    { cwd: root, maxBuffer: 64 * 1024 * 1024 },
  );
  return stdout.split("\0").filter(Boolean);
}

const failures = [];
for (const repoPath of (await listCommittableFiles()).sort()) {
  if (!shouldCheck(repoPath)) continue;
  const filePath = path.join(root, repoPath);
  let text;
  try {
    text = await readFile(filePath, "utf8");
  } catch {
    continue; // deleted in the working tree, or unreadable binary listed by git
  }
  for (const finding of findPersonalPaths(text)) {
    const line = text.slice(0, finding.index).split("\n").length;
    failures.push(`${repoPath}:${line}: ${finding.match}`);
  }
}

if (failures.length) {
  console.error(`[personal-paths] ${failures.length} failure(s)`);
  for (const failure of failures) console.error(`- ${failure}`);
  process.exit(1);
}
console.log("[personal-paths] OK - no personal user profile paths (Windows/WSL/Linux-home forms) in committable text files");
