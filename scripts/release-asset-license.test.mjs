import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import test from "node:test";
import { fileURLToPath } from "node:url";
import path from "node:path";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const checker = path.join(root, "scripts", "check-release-assets.mjs");

test("release asset license boundary is reproducible", () => {
  execFileSync(process.execPath, [checker, "--check"], { cwd: root, stdio: "pipe" });
});
