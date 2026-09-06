#!/usr/bin/env node

// Tests for scripts/check-bindings-imports.mjs (P19 P1-07): the finder
// detects static/dynamic direct bindings imports, the sanction rules admit
// exactly the documented categories, and a full repository run exits clean.

import { execFileSync } from "node:child_process";
import test from "node:test";
import assert from "node:assert/strict";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

const { findDirectBindingsImports, isSanctioned } = await import(
  pathToFileURL(path.join(root, "scripts", "check-bindings-imports.mjs")).href
);

test("findDirectBindingsImports detects static, dynamic, and aliased imports", () => {
  const text = [
    `import * as S from "../../bindings/github.com/x/services/foo.js";`,
    `const mod = await import("../../bindings/github.com/x/services/bar.js");`,
    `import type { T } from "@/bindings/github.com/x/services/baz.js";`,
    `import { unrelated } from "@/api/services";`,
  ].join("\n");
  assert.deepEqual(findDirectBindingsImports(text), [
    { line: 1, kind: "static", excerpt: text.split("\n")[0] },
    { line: 2, kind: "dynamic", excerpt: text.split("\n")[1] },
    { line: 3, kind: "static", excerpt: text.split("\n")[2] },
  ]);
});

test("isSanctioned admits api wrappers, tests, e2e probes, and store lazy-loads", () => {
  assert.equal(isSanctioned("frontend/src/api/git.ts", "static").ok, true);
  assert.equal(isSanctioned("frontend/src/api/automation.ts", "dynamic").ok, true);
  assert.equal(isSanctioned("frontend/src/stores/lsp.test.ts", "static").ok, true);
  assert.equal(isSanctioned("frontend/src/e2e/workspaceProbe.ts", "static").ok, true);
  assert.equal(isSanctioned("frontend/src/stores/mcp.ts", "dynamic").ok, true);
});

test("isSanctioned rejects unregistered production imports", () => {
  assert.equal(isSanctioned("frontend/src/stores/mcp.ts", "static").ok, false);
  assert.equal(isSanctioned("frontend/src/lib/newAdapter.ts", "static").ok, false);
  assert.equal(isSanctioned("frontend/src/components/editor/Foo.vue", "static").ok, false);
  // Dynamic lazy-loads are only sanctioned inside stores/.
  assert.equal(isSanctioned("frontend/src/lib/newAdapter.ts", "dynamic").ok, false);
});

test("isSanctioned admits exactly the registered static exceptions", () => {
  assert.equal(isSanctioned("frontend/src/main.ts", "static").ok, true);
  assert.equal(isSanctioned("frontend/src/lib/codeLens.ts", "static").ok, true);
  assert.equal(isSanctioned("frontend/src/components/layout/DebugPanel.vue", "static").ok, true);
  assert.equal(isSanctioned("frontend/src/stores/snapshot.ts", "static").ok, true);
  // A typo'd or moved path must not silently stay sanctioned.
  assert.equal(isSanctioned("frontend/src/lib/codeLens2.ts", "static").ok, false);
});

test("repository-wide guard run exits clean (registry matches reality)", () => {
  execFileSync(process.execPath, [path.join(root, "scripts", "check-bindings-imports.mjs")], {
    cwd: root,
    stdio: "pipe",
  });
});
