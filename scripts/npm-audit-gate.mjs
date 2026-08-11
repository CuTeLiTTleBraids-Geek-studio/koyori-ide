#!/usr/bin/env node

/**
 * G19 dependency-governance gate.
 *
 * 1. `npm audit` against the official registry at --audit-level=high must
 *    report no high/critical advisories (a mirror that cannot audit does not
 *    count as a pass).
 * 2. The lockfile must be in sync with package.json: a `--package-lock-only
 *    --dry-run` resolve must not change package-lock.json.
 *
 * CI and local developers run this before merge/release; the release.yml
 * gate uses the same script (real CI evidence remains U until a runner
 * exists).
 */

import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const frontend = path.join(root, "frontend");
const lockPath = path.join(frontend, "package-lock.json");
const registry = "https://registry.npmjs.org";
const registryPrefix = `${registry}/`;

function npm(args) {
  if (process.platform === "win32") {
    // Windows cannot CreateProcess .cmd shims directly; args are fixed
    // literals (no user input), so cmd /c is safe.
    return spawnSync("cmd.exe", ["/c", "npm", ...args], {
      cwd: frontend,
      encoding: "utf8",
      env: { ...process.env, npm_config_registry: registry },
      timeout: 300_000,
    });
  }
  return spawnSync("npm", args, {
    cwd: frontend,
    encoding: "utf8",
    env: { ...process.env, npm_config_registry: registry },
    timeout: 300_000,
  });
}

const failures = [];

// npm ci honors package-lock.json `resolved` URLs even when --registry is
// supplied. Keep the lockfile itself on the approved registry so mirrors
// cannot silently change the download source or cache provenance.
const lockfile = JSON.parse(readFileSync(lockPath, "utf8"));
const nonOfficialResolved = Object.entries(lockfile.packages ?? {})
  .filter(([, metadata]) => typeof metadata?.resolved === "string")
  .filter(([, metadata]) => !metadata.resolved.startsWith(registryPrefix));
if (nonOfficialResolved.length > 0) {
  const sample = nonOfficialResolved.slice(0, 5).map(([location, metadata]) => `${location} -> ${metadata.resolved}`);
  failures.push(
    `package-lock.json contains ${nonOfficialResolved.length} non-official resolved URL(s); expected ${registryPrefix}:\n${sample.join("\n")}`,
  );
} else {
  console.log("[npm-gate] PASS: package-lock.json resolved URLs use the official npm registry");
}

// 1) official-registry audit at high level.
const audit = npm(["audit", "--audit-level=high", "--registry=" + registry]);
if (audit.error) {
  failures.push(`npm audit spawn failed: ${audit.error.message}`);
} else if (audit.status !== 0) {
  const tail = `${audit.stdout}\n${audit.stderr}`.trim().split("\n").slice(-12).join("\n");
  failures.push(`npm audit reports high/critical advisories (exit ${audit.status}):\n${tail}`);
} else {
  console.log("[npm-gate] PASS: official-registry npm audit --audit-level=high reports no high/critical advisories");
}

// 2) lockfile stability: a resolve-only dry run must not change the lockfile.
const before = createHash("sha256").update(readFileSync(lockPath)).digest("hex");
const resolve = npm(["install", "--package-lock-only", "--dry-run"]);
const after = createHash("sha256").update(readFileSync(lockPath)).digest("hex");
if (resolve.error) {
  failures.push(`npm install --package-lock-only --dry-run spawn failed: ${resolve.error.message}`);
} else if (resolve.status !== 0) {
  failures.push(`npm install --package-lock-only --dry-run failed (exit ${resolve.status})`);
} else if (before !== after) {
  failures.push("package-lock.json drifted after a resolve-only dry run (package.json and lockfile out of sync)");
} else {
  console.log("[npm-gate] PASS: package-lock.json is stable (resolve-only dry run produced no drift)");
}

if (failures.length > 0) {
  console.error("[npm-gate] FAIL:\n" + failures.join("\n---\n"));
  process.exit(1);
}
console.log("[npm-gate] OK - dependency governance gate passed");
