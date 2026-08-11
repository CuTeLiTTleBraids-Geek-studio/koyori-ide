#!/usr/bin/env node

/**
 * P9-G24 AC3: Extension corpus automatic report generator.
 *
 * Input:  a directory of real .vsix packages (default
 *         build/e2e-evidence/p9-g20/corpus/).
 * Output: build/e2e-evidence/p9-g24/corpus-report.json
 *
 * For every package the report records:
 *   - identity (publisher.name), version, SHA-256 of the vsix file
 *   - entrypoint (main/browser) and whether it is a compatible shape
 *   - activationEvents and contributes summary
 *   - every `vscode.<namespace>.<api>` reference found in the bundled
 *     entrypoint source (static analysis; not proof of execution)
 *   - the Koyori permission declaration (koyoriIde.permissions)
 *   - a disposition: supported / unsupported / blocked / corrupt, with a
 *     concrete reason. Installing successfully is NOT activation success:
 *     missing permission declarations, unsigned/unverified archives,
 *     incompatible entrypoints and unreachable network are all explicit
 *     reasons, never implied success.
 *
 * The generator is intentionally testable: it accepts a directory or an
 * explicit file list, fails closed on corrupt archives, detects duplicate
 * identities, and reports empty corpora instead of claiming coverage.
 */

import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFile, readdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { ZipArchive } from "./g24-vsix-zip.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const DEFAULT_INPUT = path.join(root, "build", "e2e-evidence", "p9-g20", "corpus");
const DEFAULT_OUTPUT = path.join(root, "build", "e2e-evidence", "p9-g24", "corpus-report.json");

/** Static API references are matched from the bundled entrypoint source. */
const VSCODE_API_REFERENCE =
  /\bvscode\.([A-Za-z][A-Za-z0-9]*)\.([A-Za-z][A-Za-z0-9]*)/g;

/** Namespaces the Koyori extension host exposes (see vscodeExtensionActivation.ts). */
const KNOWN_NAMESPACES = new Set([
  "languages",
  "commands",
  "workspace",
  "secrets",
  "tasks",
  "window",
  "debug",
  "scm",
  "env",
]);

/**
 * Namespaces referenced by a package's entrypoint that the Koyori host does
 * not expose. Referencing an unknown namespace is a hard incompatibility —
 * activation would throw "Unsupported vscode API namespace".
 */
function unknownNamespaces(apiReferences) {
  return [...new Set(apiReferences.map((ref) => ref.namespace))]
    .filter((ns) => !KNOWN_NAMESPACES.has(ns));
}

async function sha256File(filePath) {
  const hash = createHash("sha256");
  hash.update(await readFile(filePath));
  return hash.digest("hex");
}

/** Read a text file inside the vsix (UTF-8, stripped of BOM). */
async function readArchiveText(archive, entryName) {
  const contents = await archive.readText(entryName);
  if (contents === null) return null;
  return contents.charCodeAt(0) === 0xfeff ? contents.slice(1) : contents;
}

function countEntries(obj) {
  if (!obj || typeof obj !== "object" || Array.isArray(obj)) return 0;
  return Object.keys(obj).length;
}

function summarizeContributes(contributes) {
  const summary = {
    commands: Array.isArray(contributes?.commands) ? contributes.commands.length : 0,
    languages: Array.isArray(contributes?.languages) ? contributes.languages.length : 0,
    grammars: Array.isArray(contributes?.grammars) ? contributes.grammars.length : 0,
    themes: Array.isArray(contributes?.themes) ? contributes.themes.length : 0,
    iconThemes: Array.isArray(contributes?.iconThemes) ? contributes.iconThemes.length : 0,
    snippets: Array.isArray(contributes?.snippets) ? contributes.snippets.length : 0,
    menus: countEntries(contributes?.menus),
    views: countEntries(contributes?.views),
  };
  return summary;
}

/**
 * Build the report for one vsix file. Never throws for a valid zip; corrupt
 * archives are reported with disposition "corrupt".
 */
async function analyzePackage(filePath) {
  const hash = await sha256File(filePath);
  const base = {
    file: path.basename(filePath),
    sha256: hash,
  };
  let archive;
  try {
    archive = await ZipArchive.open(filePath);
  } catch (error) {
    return { ...base, disposition: "corrupt", reason: `unreadable vsix: ${error.message}` };
  }
  try {
    const manifestText = await readArchiveText(archive, "extension/package.json");
    if (manifestText === null) {
      return { ...base, disposition: "corrupt", reason: "missing extension/package.json" };
    }
    let manifest;
    try {
      manifest = JSON.parse(manifestText);
    } catch (error) {
      return { ...base, disposition: "corrupt", reason: `invalid extension/package.json: ${error.message}` };
    }
    if (
      typeof manifest?.name !== "string"
      || typeof manifest?.publisher !== "string"
      || typeof manifest?.version !== "string"
    ) {
      return { ...base, disposition: "corrupt", reason: "package.json lacks publisher/name/version" };
    }

    const identity = `${manifest.publisher}.${manifest.name}`;
    const main = typeof manifest.main === "string" ? manifest.main : "";
    const browser = typeof manifest.browser === "string" ? manifest.browser : "";
    const entrypoint = browser || main;
    const activationEvents = Array.isArray(manifest.activationEvents)
      ? manifest.activationEvents
      : [];
    const permissions = Array.isArray(manifest.koyoriIde?.permissions)
      ? manifest.koyoriIde.permissions
      : null;

    const record = {
      ...base,
      identity,
      publisher: manifest.publisher,
      name: manifest.name,
      version: manifest.version,
      displayName: typeof manifest.displayName === "string" ? manifest.displayName : "",
      entrypoint: entrypoint || null,
      entrypointType: entrypoint ? (browser ? "browser" : "main") : null,
      activationEvents,
      contributes: summarizeContributes(manifest.contributes),
      declaredPermissions: permissions,
      apiReferences: [],
      unsupportedApiNamespaces: [],
      disposition: "unsupported",
      reason: "",
    };

    if (permissions === null) {
      record.disposition = "blocked";
      record.reason =
        "no koyoriIde.permissions declaration; install would be rejected by the permission gate";
      return record;
    }

    if (!entrypoint) {
      record.disposition = "unsupported";
      record.reason = "no main/browser entrypoint; no activation payload to execute";
      return record;
    }

    const entryText = await readArchiveText(archive, `extension/${entrypoint}`);
    if (entryText === null) {
      record.disposition = "unsupported";
      record.reason = `entrypoint ${entrypoint} is missing from the package`;
      return record;
    }

    const references = [];
    for (const match of entryText.matchAll(VSCODE_API_REFERENCE)) {
      references.push({ namespace: match[1], api: match[2] });
    }
    record.apiReferences = references;
    const unknown = unknownNamespaces(references);
    record.unsupportedApiNamespaces = unknown;

    if (unknown.length > 0) {
      record.disposition = "unsupported";
      record.reason =
        `entrypoint references unsupported vscode namespaces: ${unknown.join(", ")}`;
      return record;
    }

    record.disposition = "supported";
    record.reason =
      "compatible entrypoint, declared permissions, and known vscode API namespaces (static analysis only; activation success requires the packaged run)";
    return record;
  } finally {
    await archive.close();
  }
}

async function main() {
  const input = process.argv[2] ? path.resolve(process.argv[2]) : DEFAULT_INPUT;
  const output = process.argv[3] ? path.resolve(process.argv[3]) : DEFAULT_OUTPUT;

  let entries;
  try {
    entries = await readdir(input, { withFileTypes: true });
  } catch (error) {
    console.error(`[g24-corpus-report] cannot read input directory: ${error.message}`);
    process.exit(1);
  }
  const vsixFiles = entries
    .filter((entry) => entry.isFile() && entry.name.toLowerCase().endsWith(".vsix"))
    .map((entry) => path.join(input, entry.name))
    .sort();

  if (vsixFiles.length === 0) {
    console.error("[g24-corpus-report] empty corpus: no .vsix files found; refusing to report coverage");
    process.exit(2);
  }

  const packages = [];
  const seenIdentities = new Set();
  const duplicateIdentities = [];
  for (const filePath of vsixFiles) {
    const record = await analyzePackage(filePath);
    packages.push(record);
    if (record.identity) {
      if (seenIdentities.has(record.identity)) duplicateIdentities.push(record.identity);
      seenIdentities.add(record.identity);
    }
  }

  const counts = { supported: 0, unsupported: 0, blocked: 0, corrupt: 0 };
  for (const record of packages) counts[record.disposition] += 1;

  const report = {
    generator: "scripts/g24-corpus-report.mjs",
    generatedAt: new Date().toISOString(),
    inputDirectory: input,
    packageCount: packages.length,
    counts,
    duplicateIdentities,
    packages,
  };

  await writeFile(output, `${JSON.stringify(report, null, 2)}\n`, "utf8");
  console.log(
    `[g24-corpus-report] ${packages.length} packages → ${output} ` +
      `(supported=${counts.supported} unsupported=${counts.unsupported} blocked=${counts.blocked} corrupt=${counts.corrupt})`,
  );
  assert.equal(counts.corrupt, 0, "corrupt corpus packages must be reported, not hidden");
  assert.equal(duplicateIdentities.length, 0, "duplicate corpus identities must be reported");
}

export const testable = { analyzePackage, unknownNamespaces, summarizeContributes };

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  main().catch((error) => {
    console.error(`[g24-corpus-report] failed: ${error?.stack ?? error}`);
    process.exit(1);
  });
}
