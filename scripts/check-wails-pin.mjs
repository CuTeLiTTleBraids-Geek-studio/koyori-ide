#!/usr/bin/env node

import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const modulePath = "github.com/wailsapp/wails/v3";
const goMod = await readFile(path.join(root, "go.mod"), "utf8");
const match = goMod.match(/^\s*github\.com\/wailsapp\/wails\/v3\s+(v\S+)/m);

assert(match, `[wails-pin] ${modulePath} is not pinned in go.mod`);
const pinnedVersion = match[1];
assert(!/[xX*]|latest/i.test(pinnedVersion), `[wails-pin] invalid floating version: ${pinnedVersion}`);

const pinDeclarationFiles = [
  ".github/workflows/ci.yml",
  ".github/workflows/release.yml",
  ".github/workflows/release-installers.yml",
  "build/scripts/build-windows.ps1",
  "docs/RELEASING.md",
  "docs/E2E.md",
];

for (const relative of pinDeclarationFiles) {
  const source = await readFile(path.join(root, relative), "utf8");
  assert(
    !/go install\s+github\.com\/wailsapp\/wails\/v3\/cmd\/wails3@latest\b/i.test(source),
    `[wails-pin] ${relative} installs the Wails CLI with @latest`,
  );
  const cliPins = [...source.matchAll(/wails\/v3\/cmd\/wails3@(v[^\s`"']+)/g)].map(
    (entry) => entry[1],
  );
  assert(cliPins.length > 0, `[wails-pin] ${relative} does not declare the Wails CLI pin`);
  for (const cliPin of cliPins) {
    assert.equal(
      cliPin,
      pinnedVersion,
      `[wails-pin] ${relative} uses ${cliPin}; go.mod uses ${pinnedVersion}`,
    );
  }
}

const manifest = JSON.parse(
  await readFile(path.join(root, "scripts/wails-bindings.manifest.json"), "utf8"),
);
assert.equal(
  manifest?.generator?.module,
  modulePath,
  "[wails-pin] binding manifest uses the wrong generator module",
);
assert.equal(
  manifest?.generator?.version,
  pinnedVersion,
  `[wails-pin] binding manifest uses ${manifest?.generator?.version}; go.mod uses ${pinnedVersion}`,
);
assert.equal(
  manifest?.strategy,
  "untracked-generate-before-use",
  "[wails-pin] binding manifest must declare the untracked generation strategy",
);

const operationalBindingFiles = [
  "Taskfile.yml",
  "build/Taskfile.yml",
  "build/docker/Dockerfile.cross",
  "build/ios/Taskfile.yml",
  "build/android/Taskfile.yml",
  "build/scripts/build-macos.sh",
  "build/scripts/build-linux.sh",
  "build/scripts/build-windows.ps1",
  "build/scripts/wsl-cross-macos-desktop.sh",
  "build/scripts/wsl-package-all.sh",
  "scripts/build-windows-gui.ps1",
  "scripts/dev-setup.ps1",
  "scripts/dev-setup.sh",
  "frontend/package.json",
  ".github/workflows/ci.yml",
  ".github/workflows/package.yml",
  ".github/workflows/release.yml",
  ".github/workflows/release-installers.yml",
];
for (const relative of operationalBindingFiles) {
  const source = await readFile(path.join(root, relative), "utf8");
  assert(
    !/wails3\s+generate\s+bindings\b/i.test(source),
    `[wails-pin] ${relative} bypasses scripts/generate-bindings.mjs`,
  );
}

for (const relative of [
  "build/Taskfile.yml",
  "build/docker/Dockerfile.cross",
  "build/ios/Taskfile.yml",
  "build/android/Taskfile.yml",
  "build/scripts/build-macos.sh",
  "build/scripts/build-linux.sh",
  "build/scripts/build-windows.ps1",
  "build/scripts/wsl-cross-macos-desktop.sh",
  "build/scripts/wsl-package-all.sh",
  "scripts/build-windows-gui.ps1",
  "scripts/dev-setup.ps1",
  "scripts/dev-setup.sh",
  "frontend/package.json",
  ".github/workflows/ci.yml",
  ".github/workflows/package.yml",
  ".github/workflows/release.yml",
  ".github/workflows/release-installers.yml",
]) {
  const source = await readFile(path.join(root, relative), "utf8");
  assert(
    /generate-bindings\.mjs/.test(source),
    `[wails-pin] ${relative} does not generate bindings through the pinned entry point`,
  );
}

const gitignore = await readFile(path.join(root, ".gitignore"), "utf8");
assert(
  /^frontend\/bindings\/$/m.test(gitignore),
  "[wails-pin] .gitignore must ignore the complete generated bindings tree",
);
assert(
  !/^frontend\/bindings\/koyori-ide\/$/m.test(gitignore),
  "[wails-pin] mixed tracked/untracked binding ownership is forbidden",
);
assert(
  /^\/wails_obfuscated\.gen\.go$/m.test(gitignore),
  "[wails-pin] generated obfuscated binding metadata must be untracked",
);

console.log(
  `[wails-pin] OK - go.mod, manifest, CI/docs, and generation entry points use ${pinnedVersion}`,
);
