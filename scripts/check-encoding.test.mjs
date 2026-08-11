import { execFileSync } from "node:child_process";
import test from "node:test";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

test("repository text is valid UTF-8 without accidental replacement characters", () => {
  execFileSync(process.execPath, [path.join(root, "scripts", "check-encoding.mjs")], {
    cwd: root,
    stdio: "pipe",
  });
});
