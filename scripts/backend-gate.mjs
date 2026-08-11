#!/usr/bin/env node

// P9-G07 执行点 3 / AC4: one-click Windows backend gate that runs each
// sub-command directly (no pipe masking) and reports its own exit code.
// Also fails if `go test ./...` reports any `[no tests to run]` package, so
// an empty test binary can never be counted as a passing race/coverage run.

import { spawnSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const node = process.execPath;

const steps = [
  {
    name: "gofmt -l . (must be empty)",
    command: "gofmt",
    args: ["-l", "."],
    cwd: root,
    validate: (result) => {
      const files = result.stdout.trim();
      if (files !== "") throw new Error(`unformatted Go files:\n${files}`);
    },
  },
  { name: "go vet ./...", command: "go", args: ["vet", "./..."], cwd: root },
  { name: "go build -buildvcs=false ./...", command: "go", args: ["build", "-buildvcs=false", "./..."], cwd: root },
  {
    name: "go test ./... -count=1",
    command: "go",
    args: ["test", "./...", "-count=1"],
    cwd: root,
    timeoutMs: 30 * 60 * 1000,
    validate: (result) => {
      const combined = `${result.stdout}\n${result.stderr}`;
      if (combined.includes("[no tests to run]")) {
        throw new Error("go test reported [no tests to run]; an empty test binary must not be counted as a pass");
      }
    },
  },
  { name: "node scripts/contract-smoke.mjs", command: node, args: ["scripts/contract-smoke.mjs"], cwd: root },
  { name: "node scripts/check-bindings.mjs", command: node, args: ["scripts/check-bindings.mjs"], cwd: root },
  { name: "node scripts/check-wails-pin.mjs", command: node, args: ["scripts/check-wails-pin.mjs"], cwd: root },
  { name: "node scripts/check-doc-links.mjs", command: node, args: ["scripts/check-doc-links.mjs"], cwd: root },
  { name: "node scripts/check-doc-numbers.mjs", command: node, args: ["scripts/check-doc-numbers.mjs"], cwd: root },
];

let failed = false;
const results = [];
for (const step of steps) {
  const startedAt = Date.now();
  const result = spawnSync(step.command, step.args, {
    cwd: step.cwd ?? root,
    encoding: "utf8",
    shell: false,
    windowsHide: true,
    timeout: step.timeoutMs ?? 15 * 60 * 1000,
    maxBuffer: 64 * 1024 * 1024,
  });
  const elapsed = ((Date.now() - startedAt) / 1000).toFixed(1);
  let ok = result.status === 0;
  let detail = "";
  if (ok && step.validate) {
    try {
      step.validate(result);
    } catch (error) {
      ok = false;
      detail = error instanceof Error ? error.message : String(error);
    }
  }
  if (!ok && detail === "") {
    const tail = `${result.stdout ?? ""}\n${result.stderr ?? ""}`.trim().split(/\r?\n/).slice(-12).join("\n");
    detail = tail;
  }
  results.push({ name: step.name, ok, exit: result.status, signal: result.signal, elapsed, detail });
  if (!ok) failed = true;
  console.log(`[backend-gate] ${ok ? "PASS" : "FAIL"} (exit=${result.status ?? "null"}, ${elapsed}s) ${step.name}`);
  if (detail) console.log(detail.split("\n").map((line) => `  | ${line}`).join("\n"));
}

console.log("[backend-gate] summary:");
for (const r of results) {
  console.log(`  ${r.ok ? "PASS" : "FAIL"} exit=${r.exit} ${r.elapsed}s  ${r.name}`);
}
if (failed) {
  console.error("[backend-gate] FAILED: one or more backend gates are red");
  process.exit(1);
}
console.log("[backend-gate] OK - all backend gates passed with per-command exit codes");