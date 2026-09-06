#!/usr/bin/env node

/**
 * Dependency-governance gate.
 *
 * 1. Every registry package must pin an official-registry URL and SRI digest.
 * 2. The official registry audit must report no high/critical advisories.
 * 3. `npm ci --dry-run` must accept package.json and package-lock.json as a
 *    synchronized pair without executing lifecycle scripts.
 */

import { spawnSync } from "node:child_process";
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const frontend = path.join(root, "frontend");
const lockPath = path.join(frontend, "package-lock.json");
export const registry = "https://registry.npmjs.org";
const registryPrefix = `${registry}/`;
export const auditArgs = Object.freeze([
  "audit",
  "--package-lock-only",
  "--audit-level=high",
  "--registry=" + registry,
]);
export const syncArgs = Object.freeze([
  "ci",
  "--dry-run",
  "--ignore-scripts",
  "--no-audit",
]);

export function npm(
  args,
  {
    spawn = spawnSync,
    platform = process.platform,
    environment = process.env,
    workingDirectory = frontend,
  } = {},
) {
  const options = {
    cwd: workingDirectory,
    encoding: "utf8",
    env: { ...environment, npm_config_registry: registry },
    timeout: 300_000,
  };
  if (platform === "win32") {
    // Arguments are fixed literals controlled by this script.
    return spawn("cmd.exe", ["/d", "/s", "/c", "npm", ...args], options);
  }
  return spawn("npm", args, options);
}

function outputTail(result) {
  return `${result.stdout ?? ""}\n${result.stderr ?? ""}`
    .trim()
    .split("\n")
    .slice(-12)
    .join("\n");
}

export function runDependencyGate(
  lockfile,
  { npmRunner = npm, log = (message) => console.log(message) } = {},
) {
  const failures = [];
  const registryPackages = Object.entries(lockfile.packages ?? {}).filter(
    ([location, metadata]) =>
      location !== "" &&
      metadata?.link !== true &&
      metadata?.inBundle !== true &&
      typeof metadata?.version === "string",
  );

  const incompleteEntries = registryPackages.filter(
    ([, metadata]) =>
      typeof metadata?.resolved !== "string" ||
      metadata.resolved.length === 0 ||
      typeof metadata?.integrity !== "string" ||
      !/^sha(?:256|384|512)-/.test(metadata.integrity),
  );
  if (incompleteEntries.length > 0) {
    const sample = incompleteEntries
      .slice(0, 8)
      .map(
        ([location, metadata]) =>
          `${location} (resolved=${JSON.stringify(metadata?.resolved)}, integrity=${JSON.stringify(metadata?.integrity)})`,
      );
    failures.push(
      `package-lock.json has ${incompleteEntries.length} registry package(s) without a committed resolved URL and SRI digest:\n${sample.join("\n")}`,
    );
  }

  const nonOfficialResolved = registryPackages
    .filter(([, metadata]) => typeof metadata?.resolved === "string")
    .filter(([, metadata]) => !metadata.resolved.startsWith(registryPrefix));
  if (nonOfficialResolved.length > 0) {
    const sample = nonOfficialResolved
      .slice(0, 8)
      .map(([location, metadata]) => `${location} -> ${metadata.resolved}`);
    failures.push(
      `package-lock.json contains ${nonOfficialResolved.length} non-official resolved URL(s); expected ${registryPrefix}:\n${sample.join("\n")}`,
    );
  }
  if (incompleteEntries.length === 0 && nonOfficialResolved.length === 0) {
    log(
      `[npm-gate] PASS: ${registryPackages.length} registry packages pin official URLs and integrity metadata`,
    );
  }

  const audit = npmRunner(auditArgs);
  if (audit.error) {
    failures.push(`npm audit spawn failed: ${audit.error.message}`);
  } else if (audit.status !== 0) {
    failures.push(
      `npm audit reports high/critical advisories (exit ${audit.status}):\n${outputTail(audit)}`,
    );
  } else {
    log(
      "[npm-gate] PASS: official-registry audit reports no high/critical advisories",
    );
  }

  const sync = npmRunner(syncArgs);
  if (sync.error) {
    failures.push(`npm ci --dry-run spawn failed: ${sync.error.message}`);
  } else if (sync.status !== 0) {
    failures.push(
      `npm ci --dry-run rejected package.json/package-lock.json (exit ${sync.status}):\n${outputTail(sync)}`,
    );
  } else {
    log(
      "[npm-gate] PASS: npm ci --dry-run accepts the committed dependency graph",
    );
  }

  return failures;
}

export function main() {
  const lockfile = JSON.parse(readFileSync(lockPath, "utf8"));
  const failures = runDependencyGate(lockfile);
  if (failures.length > 0) {
    console.error("[npm-gate] FAIL:\n" + failures.join("\n---\n"));
    return 1;
  }
  console.log("[npm-gate] OK - dependency governance gate passed");
  return 0;
}

if (path.resolve(process.argv[1] ?? "") === fileURLToPath(import.meta.url)) {
  process.exit(main());
}
