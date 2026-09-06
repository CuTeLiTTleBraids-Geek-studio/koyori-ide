import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";

import { parseExports, root } from "./lib/wails-bindings.mjs";

const goMod = await readFile(path.join(root, "go.mod"), "utf8");
const moduleMatch = /^module[ \t]+(\S+)[ \t]*$/m.exec(goMod);
assert.ok(moduleMatch, "go.mod must declare exactly one module path");
const modulePath = moduleMatch[1];
const bindingModulePath = `${modulePath}/services/httpclientservice`;

const expectedExports = [
  "CancelRequest",
  "ClearHistory",
  "GetHistory",
  "ParseHTTPEnvironment",
  "ParseHTTPFile",
  "RequestPrivateNetworkAccess",
  "SendRequest",
];

test("HTTP Client production code statically imports the generated binding", async () => {
  const source = await readFile(path.join(root, "frontend", "src", "stores", "httpClient.ts"), "utf8");
  const expectedImport = `import * as HTTPClientServiceBindings from "../../bindings/${bindingModulePath}.js";`;
  assert.ok(source.includes(expectedImport), `missing exact generated binding import: ${expectedImport}`);
  assert.doesNotMatch(source, /@vite-ignore|\bbindingPath\b|\bloadBindings\b|import\s*\(/);
});

test("generated HTTP Client binding and manifest expose the exact required surface", async () => {
  const bindingPath = path.join(
    root,
    "frontend",
    "bindings",
    ...modulePath.split("/"),
    "services",
    "httpclientservice.ts",
  );
  const [bindingSource, manifestSource] = await Promise.all([
    readFile(bindingPath, "utf8"),
    readFile(path.join(root, "scripts", "wails-bindings.manifest.json"), "utf8"),
  ]);
  const manifest = JSON.parse(manifestSource);

  assert.deepEqual(parseExports(bindingSource), expectedExports);
  assert.deepEqual(
    manifest.exports[`${bindingModulePath}.ts`],
    expectedExports,
  );
  assert.doesNotMatch(bindingSource, /\$Call\.ByName\s*\(/);
});
