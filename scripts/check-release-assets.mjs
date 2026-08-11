#!/usr/bin/env node

import { createHash } from "node:crypto";
import { readFile, readdir, stat, writeFile } from "node:fs/promises";
import { spawnSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const outputPath = path.join(root, "docs", "RELEASE_ASSET_LICENSES.md");
const checkOnly = process.argv.includes("--check");
const requireDist = process.argv.includes("--require-dist");

const publicAssets = [
  {
    path: "frontend/public/wails.png",
    license: "MIT",
    holder: "Wails contributors",
    evidence: "pinned Wails Vue template + Wails module LICENSE",
    source: "github.com/wailsapp/wails/v3@v3.0.0-alpha2.111/internal/templates/vue/frontend/public/wails.png",
  },
  {
    path: "frontend/public/vue.svg",
    license: "MIT",
    holder: "Vue contributors",
    evidence: "pinned Wails Vue template + Wails module LICENSE",
    source: "github.com/wailsapp/wails/v3@v3.0.0-alpha2.111/internal/templates/vue/frontend/public/vue.svg",
  },
];

const nativeAssets = [
  { path: "icon.png", license: "MIT", holder: "koyori-ide contributors", evidence: "repository LICENSE", source: "first-party source asset" },
  { path: "build/appicon.png", license: "MIT", holder: "koyori-ide contributors", evidence: "repository LICENSE", source: "copy of icon.png used by Wails icon generation" },
  { path: "build/windows/icon.ico", license: "MIT", holder: "koyori-ide contributors", evidence: "generated from build/appicon.png; repository LICENSE", source: "Wails generated Windows icon" },
  { path: "build/darwin/icons.icns", license: "MIT", holder: "koyori-ide contributors", evidence: "generated from build/appicon.png; repository LICENSE", source: "Wails generated macOS icon" },
];

function sha256(buffer) {
  return createHash("sha256").update(buffer).digest("hex");
}

function relative(filePath) {
  return path.relative(root, filePath).replaceAll(path.sep, "/");
}

async function readAsset(entry) {
  const absolute = path.join(root, entry.path);
  const content = await readFile(absolute).catch(() => null);
  if (!content || content.length === 0) {
    throw new Error(`required release asset is missing or empty: ${entry.path}`);
  }
  return { ...entry, bytes: content.length, sha256: sha256(content) };
}

function runGo(args) {
  const result = spawnSync("go", args, { cwd: root, encoding: "utf8" });
  if (result.error || result.status !== 0) {
    throw new Error(`go ${args.join(" ")} failed: ${result.error?.message ?? result.stderr.trim()}`);
  }
  return result.stdout.trim();
}

async function verifyWailsTemplateCopies() {
  const moduleDir = runGo(["list", "-m", "-f", "{{.Dir}}", "github.com/wailsapp/wails/v3"]);
  const sourceRoot = path.join(moduleDir, "internal", "templates", "vue", "frontend", "public");
  for (const entry of publicAssets) {
    const internalSuffix = entry.source.split("/internal/")[1];
    if (!internalSuffix) throw new Error(`invalid Wails source record: ${entry.source}`);
    const sourcePath = path.join(moduleDir, "internal", internalSuffix);
    const source = await readFile(sourcePath).catch(() => null);
    const target = await readFile(path.join(root, entry.path));
    if (!source || sha256(source) !== sha256(target)) {
      throw new Error(`Wails template asset differs from pinned source: ${entry.path}`);
    }
    if (!sourcePath.startsWith(sourceRoot)) {
      throw new Error(`unexpected Wails template source path: ${sourcePath}`);
    }
  }
}

async function verifyPublicAllowlist() {
  const publicDir = path.join(root, "frontend", "public");
  const actual = (await readdir(publicDir, { withFileTypes: true }))
    .filter((entry) => entry.isFile())
    .map((entry) => relative(path.join(publicDir, entry.name)))
    .sort();
  const expected = publicAssets.map((entry) => entry.path).sort();
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    throw new Error(`frontend/public asset set drifted; expected ${expected.join(", ")}, found ${actual.join(", ")}`);
  }
}

async function verifyDist() {
  const distDir = path.join(root, "frontend", "dist");
  const exists = await stat(distDir).then(() => true).catch(() => false);
  if (!exists) {
    if (requireDist) throw new Error("frontend/dist is required by --require-dist");
    return;
  }
  const distRootFiles = (await readdir(distDir, { withFileTypes: true }))
    .filter((entry) => entry.isFile())
    .map((entry) => entry.name)
    .sort();
  const expectedDistRootFiles = ["index.html", ...publicAssets.map((entry) => path.basename(entry.path))].sort();
  if (JSON.stringify(distRootFiles) !== JSON.stringify(expectedDistRootFiles)) {
    throw new Error(`frontend/dist root asset set drifted; expected ${expectedDistRootFiles.join(", ")}, found ${distRootFiles.join(", ")}`);
  }
  for (const entry of publicAssets) {
    const source = await readFile(path.join(root, entry.path));
    const dist = await readFile(path.join(distDir, entry.path.replace("frontend/public/", ""))).catch(() => null);
    if (!dist || sha256(source) !== sha256(dist)) {
      throw new Error(`frontend/dist does not contain the exact public asset: ${entry.path}`);
    }
  }
  const assetsDir = path.join(distDir, "assets");
  const codicons = (await readdir(assetsDir, { withFileTypes: true }).catch(() => []))
    .filter((entry) => entry.isFile() && /^codicon-[^/]+\.ttf$/.test(entry.name));
  if (codicons.length !== 1) {
    throw new Error(`expected exactly one bundled Monaco Codicon font, found ${codicons.length}`);
  }
}

function render(entries) {
  const lines = [
    "# Release Asset License Boundary",
    "",
    "> Generated by `node scripts/check-release-assets.mjs`. Do not edit the table by hand.",
    "",
    "This is an engineering attribution record, not legal advice. It covers the files intentionally copied into the desktop frontend or native application bundles.",
    "Unused public assets are rejected by the checker instead of silently becoming part of a release.",
    "",
    "## Asset Records",
    "",
    "| Path | Bytes | SHA-256 | License | Holder | Evidence / source |",
    "|---|---:|---|---|---|---|",
    ...entries.map((entry) => `| \`${entry.path}\` | ${entry.bytes} | \`${entry.sha256}\` | ${entry.license} | ${entry.holder} | ${entry.evidence}; ${entry.source} |`),
    "",
    "## Generated Assets",
    "",
    "The Windows ICO and macOS ICNS files are generated from the first-party `build/appicon.png` by the pinned Wails icon generator. Frontend builds must also contain exactly one `assets/codicon-*.ttf`; it is emitted by `monaco-editor` and is covered by the Monaco MIT license and `ThirdPartyNotices.txt`.",
    "",
    "The release workflow attaches this record together with `NOTICE`, the dependency inventory, per-artifact SPDX SBOMs, provenance, and checksums.",
    "",
  ];
  return lines.join("\n");
}

await verifyPublicAllowlist();
await verifyWailsTemplateCopies();
await verifyDist();

const entries = [];
for (const entry of [...publicAssets, ...nativeAssets]) entries.push(await readAsset(entry));
if (sha256(await readFile(path.join(root, "icon.png"))) !== sha256(await readFile(path.join(root, "build/appicon.png")))) {
  throw new Error("icon.png and build/appicon.png differ");
}
const rendered = render(entries);
if (checkOnly) {
  const committed = await readFile(outputPath, "utf8").catch(() => "");
  if (committed !== rendered) throw new Error("docs/RELEASE_ASSET_LICENSES.md is stale");
  console.log("[release-assets] OK");
} else {
  await writeFile(outputPath, rendered, "utf8");
  console.log(`[release-assets] wrote ${outputPath}`);
}
