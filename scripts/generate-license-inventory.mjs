#!/usr/bin/env node

import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { readFile, readdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const outputPath = path.join(root, "docs", "THIRD_PARTY_LICENSES.md");
const checkOnly = process.argv.includes("--check");
const fullCheck = process.argv.includes("--full-check");
if (checkOnly && fullCheck) {
  throw new Error("--check and --full-check are mutually exclusive");
}

// package-lock v3 omits license metadata for these installed packages. Each
// value was checked against the corresponding package.json in npm ci output.
const npmLicenseOverrides = new Map([
  ["brace-expansion@1.1.18", "MIT"],
  ["brace-expansion@2.1.4", "MIT"],
  ["brace-expansion@5.0.9", "MIT"],
  ["dompurify@3.4.12", "(MPL-2.0 OR Apache-2.0)"],
  ["glob@10.5.0", "ISC"],
  ["nanoid@3.3.17", "MIT"],
  ["postcss@8.5.25", "MIT"],
]);

// This module can appear in the pinned Wails module graph without being part
// of any supported production target's compiled package closure. If a future
// target actually imports it, the unresolved row must fail the release gate.
const goLicenseExceptions = new Map([
  ["github.com/konoui/go-qsort@v0.1.0", "upstream v0.1.0 archive contains no license file; pkg.go.dev reports None detected (https://pkg.go.dev/github.com/konoui/go-qsort@v0.1.0?tab=licenses)"],
]);

// go-localereader publishes its license expression in README.md rather than
// a conventional license file. This is a reviewed source reference, not an
// inference from the package name.
const goLicenseOverrides = new Map([
  ["github.com/mattn/go-localereader@v0.0.1", {
    license: "MIT",
    source: "README.md (License section)",
  }],
]);

function compareText(left, right) {
  return left < right ? -1 : left > right ? 1 : 0;
}

function sha256(text) {
  return createHash("sha256").update(text).digest("hex");
}

function escapeCell(value) {
  return String(value).replaceAll("|", "\\|").replaceAll("\n", " ");
}

function packageNameFromLockPath(lockPath) {
  const marker = "node_modules/";
  const offset = lockPath.lastIndexOf(marker);
  if (offset < 0) return "";
  const remainder = lockPath.slice(offset + marker.length);
  const parts = remainder.split("/");
  return parts[0].startsWith("@") ? `${parts[0]}/${parts[1]}` : parts[0];
}

function classifyLicenseText(text) {
  const spdx = text.match(/SPDX-License-Identifier:\s*([^\r\n*]+)/i)?.[1]
    ?.trim()
    .replace(/^[`'"]+|[`'".]+$/g, "");
  if (spdx) return spdx;
  const normalized = text.toLowerCase();
  if (normalized.includes("bsd 3-clause license")) return "BSD-3-Clause";
  if (normalized.includes("bsd 2-clause license")) return "BSD-2-Clause";
  if (normalized.includes("apache license") && normalized.includes("version 2.0")) return "Apache-2.0";
  if (normalized.includes("mozilla public license") && normalized.includes("2.0")) return "MPL-2.0";
  if (normalized.includes("gnu lesser general public license")) return "LGPL";
  if (normalized.includes("gnu general public license")) return "GPL";
  if (normalized.includes("permission is hereby granted, free of charge")) return "MIT";
  if (
    normalized.includes("permission to use, copy, modify") &&
    normalized.includes("with or without fee is hereby granted")
  ) return "ISC";
  if (normalized.includes("neither the name of") && normalized.includes("redistribution and use")) return "BSD-3-Clause";
  if (normalized.includes("redistribution and use") && normalized.includes("this list of conditions")) return "BSD-2-Clause";
  if (normalized.includes("the unlicense") || normalized.includes("free and unencumbered software")) return "Unlicense";
  if (normalized.includes("zlib license")) return "Zlib";
  return "UNKNOWN";
}

function downloadModuleSource(module) {
  const version = module.Version;
  if (!version) {
    throw new Error(`module ${module.Path} has no version or local directory`);
  }
  const result = spawnSync("go", ["mod", "download", "-json", `${module.Path}@${version}`], {
    cwd: root,
    encoding: "utf8",
    maxBuffer: 16 * 1024 * 1024,
  });
  let downloaded;
  try {
    downloaded = JSON.parse(result.stdout);
  } catch {
    throw new Error(`go mod download returned invalid JSON for ${module.Path}@${version}`);
  }
  if (result.error || result.status !== 0 || downloaded.Error || !downloaded.Dir) {
    throw new Error(
      `cannot obtain license source for ${module.Path}@${version}: ${downloaded.Error ?? result.error?.message ?? result.stderr.trim()}`,
    );
  }
  return downloaded;
}

const releaseTargets = [
  { GOOS: "windows", GOARCH: "amd64" },
  { GOOS: "linux", GOARCH: "amd64" },
  { GOOS: "darwin", GOARCH: "amd64" },
  { GOOS: "darwin", GOARCH: "arm64" },
];

function productionModuleKeys() {
  const keys = new Set();
  for (const target of releaseTargets) {
    const result = spawnSync(
      "go",
      [
        "list",
        "-e",
        "-deps",
        "-tags",
        "desktop,production",
        "-f",
        "{{if .Module}}{{.Module.Path}}@{{.Module.Version}}{{end}}",
        "./...",
      ],
      {
        cwd: root,
        env: { ...process.env, ...target },
        encoding: "utf8",
        maxBuffer: 16 * 1024 * 1024,
      },
    );
    if (result.error || result.status !== 0) {
      throw new Error(
        `go list production dependency closure failed for ${target.GOOS}/${target.GOARCH}: ${result.error?.message ?? result.stderr.trim()}`,
      );
    }
    for (const line of result.stdout.split(/\r?\n/).map((value) => value.trim()).filter(Boolean)) {
      keys.add(line);
    }
  }
  if (!keys.size) {
    throw new Error("go list production dependency closure returned no external modules");
  }
  return keys;
}

async function goModules() {
  const usedModules = productionModuleKeys();
  const moduleJSONTemplate = [
    '{"Path":{{printf "%q" .Path}},',
    '"Version":{{printf "%q" .Version}},',
    '"Dir":{{printf "%q" .Dir}},',
    '"Main":{{.Main}},',
    '"Replace":{{if .Replace}}{',
    '"Path":{{printf "%q" .Replace.Path}},',
    '"Version":{{printf "%q" .Replace.Version}},',
    '"Dir":{{printf "%q" .Replace.Dir}}',
    '}{{else}}null{{end}}}',
  ].join("");
  const result = spawnSync("go", ["list", "-m", "-f", moduleJSONTemplate, "all"], {
    cwd: root,
    encoding: "utf8",
    maxBuffer: 16 * 1024 * 1024,
  });
  if (result.error || result.status !== 0) {
    throw new Error(`go list failed: ${result.error?.message ?? result.stderr.trim()}`);
  }
  const modules = result.stdout
    .split(/\r?\n/)
    .filter(Boolean)
    .map((line) => JSON.parse(line))
    .filter((module) => !module.Main)
    .filter((module) => usedModules.has(`${module.Path}@${module.Version}`));

  const rows = [];
  for (const module of modules) {
    let source = module.Replace ?? module;
    const moduleVersion = module.Version || source.Version || "local replacement";
    const exceptionKey = `${module.Path}@${moduleVersion}`;
    if (goLicenseExceptions.has(exceptionKey)) {
      rows.push({
        ecosystem: "Go",
        name: module.Path,
        version: moduleVersion,
        license: "UNRESOLVED",
        source: `manual exception: ${goLicenseExceptions.get(exceptionKey)}`,
      });
      continue;
    }
    const licenseOverride = goLicenseOverrides.get(exceptionKey);
    if (licenseOverride) {
      rows.push({
        ecosystem: "Go",
        name: module.Path,
        version: moduleVersion,
        license: licenseOverride.license,
        source: `manual review: ${licenseOverride.source}`,
      });
      continue;
    }
    if (!source.Dir) {
      source = downloadModuleSource(source);
    }
    let files = [];
    if (source.Dir) {
      try {
        files = (await readdir(source.Dir))
          .filter((name) => /^(licen[cs]e|copying)(\..*)?$/i.test(name))
          .sort(compareText);
      } catch {
        files = [];
      }
    }
    const licenses = new Set();
    for (const file of files) {
      const content = await readFile(path.join(source.Dir, file), "utf8");
      licenses.add(classifyLicenseText(content.slice(0, 128 * 1024)));
    }
    rows.push({
      ecosystem: "Go",
      name: module.Path,
      version: moduleVersion,
      license: licenses.size ? [...licenses].sort(compareText).join("; ") : "UNKNOWN",
      source: files.length ? files.join(", ") : "no root license file found",
    });
  }
  return rows.sort((left, right) => compareText(left.name, right.name) || compareText(left.version, right.version));
}

function npmPackages(lock) {
  const deduplicated = new Map();
  for (const [lockPath, metadata] of Object.entries(lock.packages ?? {})) {
    if (!lockPath.includes("node_modules/") || !metadata.version) continue;
    const name = packageNameFromLockPath(lockPath);
    if (!name) continue;
    const key = `${name}@${metadata.version}`;
    const declared = metadata.license || npmLicenseOverrides.get(key) || "UNKNOWN";
    const source = metadata.license ? "package-lock.json" : npmLicenseOverrides.has(key)
      ? "manual package.json override"
      : "license metadata missing";
    const existing = deduplicated.get(key);
    if (!existing || existing.license === "UNKNOWN") {
      deduplicated.set(key, {
        ecosystem: "npm",
        name,
        version: metadata.version,
        license: declared,
        source,
      });
    }
  }
  return [...deduplicated.values()]
    .sort((left, right) => compareText(left.name, right.name) || compareText(left.version, right.version));
}

function renderInventory({ goRows, npmRows, digests }) {
  const rows = [...goRows, ...npmRows];
  const unknown = rows.filter((row) => /UNKNOWN|UNRESOLVED/.test(row.license));
  const copyleft = rows.filter((row) => /(^|[^L])GPL|AGPL/i.test(row.license));
  const activeExceptionKeys = new Set(
    goRows
      .filter((row) => row.license === "UNRESOLVED")
      .map((row) => `${row.name}@${row.version}`),
  );
  const lines = [
    "# Third-Party License Inventory",
    "",
    "> Generated by `node scripts/generate-license-inventory.mjs`. Do not edit the tables by hand.",
    "",
    "This is an engineering inventory, not legal advice. It covers the union of the four supported `desktop,production` Go package closures and packages in `frontend/package-lock.json`; registry metadata and upstream license files remain authoritative.",
    "",
    "## Source Digests",
    "",
    `- \`go.mod\`: \`${digests.goMod}\``,
    `- \`go.sum\`: \`${digests.goSum}\``,
    `- \`frontend/package-lock.json\`: \`${digests.packageLock}\``,
    "",
    "## Review Summary",
    "",
    `- Go modules: ${goRows.length}`,
    `- Distinct npm package/version pairs: ${npmRows.length}`,
    `- Unknown or unclassified licenses: ${unknown.length}`,
    `- Strong-copyleft identifiers detected by the generator: ${copyleft.length}`,
    `- Documented Go source exceptions requiring release review: ${activeExceptionKeys.size}`,
    "- npm lockfile omissions were manually checked against installed package manifests and are listed below.",
    "- Missing Go module directories are obtained through `go mod download`; generation fails unless the module has a documented exception below.",
    "- A zero strong-copyleft count applies only to the classified production closure rows.",
    "- MPL, Apache, Python, BlueOak, CC0, and dual-license expressions require preservation of their upstream notices/terms when applicable.",
    "",
    "## Manual npm Metadata Overrides",
    "",
    "| Package | Reviewed license expression | Reason |",
    "|---|---|---|",
    ...[...npmLicenseOverrides.entries()]
      .sort(([left], [right]) => compareText(left, right))
      .map(([name, license]) => `| ${escapeCell(name)} | ${escapeCell(license)} | package-lock v3 entry omitted license; checked installed package.json |`),
    "",
    "## Go Source Exceptions",
    "",
    "| Module | Status | Release requirement |",
    "|---|---|---|",
    ...[...goLicenseExceptions.entries()]
      .filter(([name]) => activeExceptionKeys.has(name))
      .sort(([left], [right]) => compareText(left, right))
      .map(([name, reason]) => `| ${escapeCell(name)} | UNRESOLVED: ${escapeCell(reason)} | resolve or record an approved exception before a stable tag |`),
    "",
    "## Go Modules",
    "",
    "| Module | Version | Detected license | Evidence |",
    "|---|---|---|---|",
    ...goRows.map((row) => `| ${escapeCell(row.name)} | ${escapeCell(row.version)} | ${escapeCell(row.license)} | ${escapeCell(row.source)} |`),
    "",
    "## npm Packages",
    "",
    "| Package | Version | Declared license | Evidence |",
    "|---|---|---|---|",
    ...npmRows.map((row) => `| ${escapeCell(row.name)} | ${escapeCell(row.version)} | ${escapeCell(row.license)} | ${escapeCell(row.source)} |`),
    "",
  ];
  if (unknown.length) {
    lines.push(
      "## Unresolved Items",
      "",
      ...unknown.map((row) => `- ${row.ecosystem}: \`${row.name}@${row.version}\` (${row.source})`),
      "",
    );
  }
  return `${lines.join("\n")}\n`;
}

const [goMod, goSum, packageLockText] = await Promise.all([
  readFile(path.join(root, "go.mod"), "utf8"),
  readFile(path.join(root, "go.sum"), "utf8"),
  readFile(path.join(root, "frontend", "package-lock.json"), "utf8"),
]);
const digests = {
  goMod: sha256(goMod),
  goSum: sha256(goSum),
  packageLock: sha256(packageLockText),
};

if (checkOnly) {
  const committed = await readFile(outputPath, "utf8").catch(() => "");
  const expectedDigests = [
    `- \`go.mod\`: \`${digests.goMod}\``,
    `- \`go.sum\`: \`${digests.goSum}\``,
    `- \`frontend/package-lock.json\`: \`${digests.packageLock}\``,
  ];
  if (!committed.startsWith("# Third-Party License Inventory\n") || expectedDigests.some((line) => !committed.includes(line))) {
    console.error("[license-inventory] docs/THIRD_PARTY_LICENSES.md is missing or stale");
    process.exit(1);
  }
  console.log("[license-inventory] OK - committed source digests match dependency manifests (offline check)");
} else {
  const inventory = renderInventory({
    goRows: await goModules(),
    npmRows: npmPackages(JSON.parse(packageLockText)),
    digests,
  });
  if (fullCheck) {
    const unresolvedRows = inventory
      .split("\n")
      .filter((line) => /^\| .* \| (UNKNOWN|UNRESOLVED)(?: \||$)/.test(line));
    if (unresolvedRows.length) {
      console.error(`[license-inventory] unresolved license rows: ${unresolvedRows.length}`);
      process.exit(1);
    }
    const committed = await readFile(outputPath, "utf8").catch(() => "");
    if (committed !== inventory) {
      console.error("[license-inventory] docs/THIRD_PARTY_LICENSES.md differs from full regeneration");
      process.exit(1);
    }
    console.log("[license-inventory] OK - full regeneration matches committed inventory");
  } else {
    await writeFile(outputPath, inventory, "utf8");
    console.log("[license-inventory] wrote docs/THIRD_PARTY_LICENSES.md");
  }
}
