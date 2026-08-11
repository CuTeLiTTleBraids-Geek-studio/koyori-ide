#!/usr/bin/env node

import { readdir, readFile } from "node:fs/promises";
import path from "node:path";

import { root } from "./lib/wails-bindings.mjs";

const bindingRelative = "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/httpclientservice.ts";
const unresolvedBindingPath = "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/httpclientservice.js";

async function listJavaScriptFiles(directory) {
  const files = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const full = path.join(directory, entry.name);
    if (entry.isDirectory()) files.push(...await listJavaScriptFiles(full));
    else if (entry.isFile() && entry.name.endsWith(".js")) files.push(full);
  }
  return files;
}

function parseBindingIDs(source) {
  const ids = new Map();
  const pattern = /export function\s+(\w+)\s*\([^]*?return \$Call\.ByID\((\d+)/g;
  for (const match of source.matchAll(pattern)) ids.set(match[1], match[2]);
  return ids;
}

try {
  const bindingPath = path.join(root, "frontend", "bindings", ...bindingRelative.split("/"));
  const manifestPath = path.join(root, "scripts", "wails-bindings.manifest.json");
  const distDirectory = path.join(root, "frontend", "dist");
  const [bindingSource, manifestSource, bundleFiles] = await Promise.all([
    readFile(bindingPath, "utf8"),
    readFile(manifestPath, "utf8"),
    listJavaScriptFiles(distDirectory),
  ]);
  const manifest = JSON.parse(manifestSource);
  const expectedExports = manifest.exports?.[bindingRelative];
  if (!Array.isArray(expectedExports) || expectedExports.length === 0) {
    throw new Error(`[http-client-production] manifest has no exports for ${bindingRelative}`);
  }
  const bindingIDs = parseBindingIDs(bindingSource);
  const missingGeneratedIDs = expectedExports.filter((name) => !bindingIDs.has(name));
  if (missingGeneratedIDs.length > 0) {
    throw new Error(
      `[http-client-production] generated binding has no ByID for: ${missingGeneratedIDs.join(", ")}`,
    );
  }

  const bundleSources = await Promise.all(bundleFiles.map(async (file) => ({
    file,
    source: await readFile(file, "utf8"),
  })));
  const unresolved = bundleSources.filter(({ source }) => source.includes(unresolvedBindingPath));
  if (unresolved.length > 0) {
    throw new Error(
      `[http-client-production] unresolved runtime binding path in: ${unresolved.map(({ file }) => path.relative(root, file)).join(", ")}`,
    );
  }
  const missingBundleIDs = [...bindingIDs.entries()].filter(([, id]) => (
    !bundleSources.some(({ source }) => source.includes(id))
  ));
  if (missingBundleIDs.length > 0) {
    throw new Error(
      `[http-client-production] production bundle omitted generated calls: ${missingBundleIDs.map(([name]) => name).join(", ")}`,
    );
  }
  console.log(
    `[http-client-production] OK - ${bindingIDs.size} generated calls bundled, unresolved path=0`,
  );
} catch (error) {
  console.error(error instanceof Error ? error.message : String(error));
  process.exitCode = 1;
}
