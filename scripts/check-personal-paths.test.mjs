import { execFileSync } from "node:child_process";
import test from "node:test";
import assert from "node:assert/strict";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { findPersonalPaths } from "./check-personal-paths.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

// Sample paths are assembled from parts so this test file itself never
// contains a literal personal-path match for the repository-wide check.
const BS = String.fromCharCode(92);
const win = (segment) => `C:${BS}Users${BS}${segment}`;
const nix = (segment) => ["C:", "Users", segment].join("/");

test("flags real Windows user profile paths", () => {
  assert.deepEqual(
    findPersonalPaths(`WAILS3_BIN=${win("Cute_")}${BS}go${BS}bin${BS}wails3.exe`).map((f) => f.match),
    [win("Cute_")],
  );
  assert.equal(findPersonalPaths(`workspace ${nix("Alice")}/Downloads/Gugacode-main`).length, 1);
  assert.equal(findPersonalPaths(`dir ${win("john.doe")}${BS}AppData`).length, 1);
});

test("does not flag placeholders, generics, or non-user C: paths", () => {
  assert.deepEqual(findPersonalPaths(`已跟踪文件中出现 ${win("<具体用户名>")} 模式即失败`), []);
  assert.deepEqual(findPersonalPaths(`锁定 %USERPROFILE%${BS}go${BS}bin${BS}wails3.exe 后通过`), []);
  assert.deepEqual(findPersonalPaths(`fixture want ${nix("example")}`), []);
  assert.deepEqual(findPersonalPaths(`env LOCALAPPDATA ${win("dev")}${BS}AppData${BS}Local`), []);
  assert.deepEqual(findPersonalPaths("payload 恰为绑定根 file:///C:/fixture-root"), []);
  assert.deepEqual(findPersonalPaths(`UNC ${BS}${BS}SERVER${BS}SHARE${BS}Repo ${nix("main.go")}`), []);
});

test("repository currently contains no personal user profile paths", () => {
  execFileSync(process.execPath, [path.join(root, "scripts", "check-personal-paths.mjs")], {
    cwd: root,
    stdio: "pipe",
  });
});
