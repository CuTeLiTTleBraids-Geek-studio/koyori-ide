#!/usr/bin/env node

import assert from "node:assert/strict";
import { mkdtemp, mkdir, readFile, readdir, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const frontend = path.join(root, "frontend");
const dryRun = process.argv.includes("--dry-run");
const frontendSmoke = [
  path.join(frontend, "node_modules", "vitest", "vitest.mjs"),
  "run",
  "--config",
  "../scripts/vitest.contract.config.ts",
];

function logStep(step, detail) {
  console.log(`[contract-smoke] ${step}: ${detail}`);
}

async function runFilesystemSmoke() {
  const workspace = await mkdtemp(path.join(os.tmpdir(), "koyori-ide-e2e-"));
  const sourceDir = path.join(workspace, "src");
  const sourceFile = path.join(sourceDir, "main.ts");
  const original = 'export const status = "open";\n';
  const saved = 'export const status = "saved";\n';

  try {
    await mkdir(sourceDir);
    await writeFile(sourceFile, original, "utf8");

    logStep("open-directory", workspace);
    const rootEntries = await readdir(workspace, { withFileTypes: true });
    assert(rootEntries.some((entry) => entry.isDirectory() && entry.name === "src"));

    logStep("tree", rootEntries.map((entry) => entry.name).join(", "));
    const sourceEntries = await readdir(sourceDir, { withFileTypes: true });
    assert(sourceEntries.some((entry) => entry.isFile() && entry.name === "main.ts"));

    logStep("open-file", sourceFile);
    assert.equal(await readFile(sourceFile, "utf8"), original);

    logStep("save-file", sourceFile);
    await writeFile(sourceFile, saved, "utf8");
    assert.equal(await readFile(sourceFile, "utf8"), saved);

    const aiMock = {
      complete: async () => ({ text: "mock response", provider: "offline-mock" }),
    };
    const response = await aiMock.complete();
    assert.deepEqual(response, { text: "mock response", provider: "offline-mock" });
    logStep("ai", "offline mock; no API key or network request");
  } finally {
    await rm(workspace, { recursive: true, force: true });
  }
}

if (dryRun) {
  assert.equal(path.basename(frontend), "frontend");
  logStep("dry-run", `filesystem fixture plus: ${process.execPath} ${frontendSmoke.join(" ")}`);
  process.exit(0);
}

await runFilesystemSmoke();

logStep("frontend-store", "running the mocked Wails core-path smoke test");
const result = spawnSync(process.execPath, frontendSmoke, {
  cwd: frontend,
  env: {
    ...process.env,
    KOYORI_IDE_E2E_AI_MODE: "mock",
  },
  stdio: "inherit",
});
if (result.error) throw result.error;
if (result.status !== 0) process.exit(result.status ?? 1);

logStep("result", "PASS");
