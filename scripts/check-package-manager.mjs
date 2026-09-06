#!/usr/bin/env node

// Single-package-manager guard (P19 P0-03 / AC-03): npm is the only supported
// frontend package manager. A stray pnpm lockfile resolves a different
// dependency tree from the same package.json (e.g. vue-router 5.2 vs 5.1),
// so its mere presence fails the check.

import { access } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

const forbidden = [
  "frontend/pnpm-lock.yaml",
  "frontend/pnpm-workspace.yaml",
  "frontend/yarn.lock",
];

const failures = [];
for (const rel of forbidden) {
  try {
    await access(path.join(root, rel));
    failures.push(rel);
  } catch {
    // absent — good
  }
}

if (failures.length) {
  console.error("[package-manager] frontend must use npm only; found:");
  for (const failure of failures) console.error(`- ${failure}`);
  console.error("Delete the file(s) and run `npm install` inside frontend/.");
  process.exit(1);
}
console.log("[package-manager] OK - npm is the only frontend package manager");
