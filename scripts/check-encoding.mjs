#!/usr/bin/env node

import { readFile, readdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const textExtensions = new Set([
  ".css", ".go", ".html", ".js", ".json", ".md", ".mjs", ".plist", ".ps1",
  ".sh", ".svg", ".ts", ".tsx", ".txt", ".vue", ".xml", ".yaml", ".yml",
]);
const textBasenames = new Set(["LICENSE", "NOTICE", "VERSION", ".gitignore"]);
const excludedDirectories = new Set([".git", "coverage", "dist", "node_modules"]);

function relative(filePath) {
  return path.relative(root, filePath).replaceAll(path.sep, "/");
}

function shouldCheck(filePath) {
  const rel = relative(filePath);
  if (rel.startsWith("build/e2e-evidence/")) return false;
  if (rel.startsWith("docs/prompts/")) return false;
  const name = path.basename(filePath);
  return textBasenames.has(name) || textExtensions.has(path.extname(name).toLowerCase());
}

async function walk(directory) {
  const files = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    if (entry.isDirectory() && excludedDirectories.has(entry.name)) continue;
    const child = path.join(directory, entry.name);
    if (entry.isDirectory()) files.push(...await walk(child));
    else if (entry.isFile() && shouldCheck(child)) files.push(child);
  }
  return files;
}

const decoder = new TextDecoder("utf-8", { fatal: true });
const failures = [];
for (const filePath of (await walk(root)).sort()) {
  const rel = relative(filePath);
  let text;
  try {
    text = decoder.decode(await readFile(filePath));
  } catch (error) {
    failures.push(`${rel}: invalid UTF-8 (${error.message})`);
    continue;
  }
  if (text.includes("\uFFFD")) failures.push(`${rel}: contains U+FFFD replacement character`);
}

if (failures.length) {
  console.error(`[encoding] ${failures.length} failure(s)`);
  for (const failure of failures) console.error(`- ${failure}`);
  process.exit(1);
}
console.log("[encoding] OK - checked UTF-8 text outside the prompt archive");
