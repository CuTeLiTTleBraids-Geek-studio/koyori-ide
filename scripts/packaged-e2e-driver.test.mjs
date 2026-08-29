import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import {
  mkdir,
  mkdtemp,
  readFile,
  rename,
  rm,
  stat,
  symlink,
  writeFile,
} from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import {
  CORE_FIXTURE_IDS,
  PackagedE2EClient,
  runCoreFixtures,
} from "./packaged-e2e-driver.mjs";
import {
  FRONTEND_E2E_PROBE_MARKERS,
  assertSourceFingerprintUnchanged,
  captureWorkingTreeEvidence,
  collectSourceFingerprintFiles,
  createPackagedE2EManifest,
  markPackagedE2EManifestFailed,
  packagedE2EFixtureResultPatch,
  runPackagedE2EManifestLifecycle,
  sourceFingerprint,
  validateReusableArtifactEvidence,
  verifyFrontendE2EProbeMarkers,
  verifyWindowsPackagedE2EEvidence,
} from "./packaged-e2e.mjs";

test("source fingerprint recursively covers untracked build inputs", async (t) => {
  const fixtureRoot = await mkdtemp(
    path.join(os.tmpdir(), "koyori-source-fingerprint-"),
  );
  t.after(async () => rm(fixtureRoot, { recursive: true, force: true }));
  for (const directory of [
    "services",
    "internal/agentcore",
    "frontend/src",
    "frontend/src/stores/node_modules/.vite/deps",
    "frontend/bindings/services",
    "frontend/.bindings-tmp-123/services",
    "frontend/.probe-bindings-deadbeef/services",
    "frontend/node_modules/pkg",
    "frontend/dist/assets",
    "scripts",
    "build/e2e-evidence/packaged-e2e",
  ]) {
    await mkdir(path.join(fixtureRoot, directory), { recursive: true });
  }
  const files = new Map([
    ["go.mod", "module example.invalid/test\n"],
    ["main.go", "package main\n"],
    [
      "services/agent_execution_core.go",
      "package services\nconst revision = 1\n",
    ],
    ["internal/agentcore/runtime.go", "package agentcore\n"],
    ["frontend/src/main.ts", "export const app = true\n"],
    [
      "frontend/src/stores/node_modules/.vite/deps/cache.js",
      "ignored nested cache\n",
    ],
    ["scripts/packaged-e2e.mjs", "export {}\n"],
    ["frontend/node_modules/pkg/index.js", "ignored dependency\n"],
    ["frontend/bindings/services/generated.ts", "ignored generated binding\n"],
    [
      "frontend/.bindings-tmp-123/services/generated.ts",
      "ignored temporary binding\n",
    ],
    [
      "frontend/.probe-bindings-deadbeef/services/generated.ts",
      "ignored binding probe\n",
    ],
    ["frontend/dist/assets/app.js", "ignored generated asset\n"],
    ["build/g03-helper.test.exe", "ignored test binary\n"],
    ["build/g03-manual-marker", "ignored test marker\n"],
    ["build/overlay_windows.json", "ignored generated overlay\n"],
    ["build/e2e-evidence/packaged-e2e/manifest.json", "{}\n"],
  ]);
  for (const [relative, content] of files) {
    await writeFile(path.join(fixtureRoot, relative), content);
  }

  const discovered = await collectSourceFingerprintFiles(fixtureRoot);
  assert(discovered.includes("services/agent_execution_core.go"));
  assert(discovered.includes("internal/agentcore/runtime.go"));
  assert(!discovered.some((relative) => relative.includes("node_modules")));
  assert(!discovered.some((relative) => relative.includes(".vite")));
  assert(!discovered.some((relative) => relative.includes("/bindings/")));
  assert(!discovered.some((relative) => relative.includes(".bindings-tmp-")));
  assert(!discovered.some((relative) => relative.includes(".probe-bindings-")));
  assert(!discovered.some((relative) => relative.includes("frontend/dist")));
  assert(!discovered.some((relative) => relative.includes("e2e-evidence")));
  assert(!discovered.some((relative) => relative.endsWith(".test.exe")));
  assert(!discovered.includes("build/g03-manual-marker"));
  assert(!discovered.includes("build/overlay_windows.json"));

  const before = await sourceFingerprint(fixtureRoot);
  await writeFile(
    path.join(fixtureRoot, "services/agent_execution_core.go"),
    "package services\nconst revision = 2\n",
  );
  const after = await sourceFingerprint(fixtureRoot);
  assert.notEqual(after, before);
});

test("working-tree evidence binds HEAD plus dirty porcelain (P13-G05)", async (t) => {
  const fixtureRoot = await mkdtemp(
    path.join(os.tmpdir(), "koyori-dirty-tree-"),
  );
  t.after(async () => rm(fixtureRoot, { recursive: true, force: true }));
  const { spawnSync } = await import("node:child_process");
  const git = (args) => {
    const probe = spawnSync("git", args, {
      cwd: fixtureRoot,
      encoding: "utf8",
    });
    assert.equal(probe.status, 0, probe.stderr || probe.stdout);
    return probe.stdout.trim();
  };
  git(["init"]);
  git(["config", "user.email", "p13@example.invalid"]);
  git(["config", "user.name", "P13"]);
  await writeFile(path.join(fixtureRoot, "tracked.txt"), "committed\n");
  git(["add", "tracked.txt"]);
  git(["commit", "-m", "initial"]);
  const clean = captureWorkingTreeEvidence(fixtureRoot);
  assert.equal(clean.gitMetadataAvailable, true);
  assert.equal(clean.workingTreeDirty, false);
  assert.equal(clean.gitStatusPorcelain, "");
  assert.equal(typeof clean.commit, "string");
  assert.equal(clean.commit.length > 0, true);

  await writeFile(path.join(fixtureRoot, "tracked.txt"), "dirty\n");
  await writeFile(path.join(fixtureRoot, "untracked.txt"), "new\n");
  const dirty = captureWorkingTreeEvidence(fixtureRoot);
  assert.equal(dirty.gitMetadataAvailable, true);
  assert.equal(dirty.workingTreeDirty, true);
  assert.match(dirty.gitStatusPorcelain, /tracked\.txt/);
  assert.match(dirty.gitStatusPorcelain, /untracked\.txt/);
  assert.notEqual(dirty.gitStatusSha256, clean.gitStatusSha256);
  assert.equal(dirty.commit, clean.commit);
});

test("working-tree evidence does not modify the Git index", async (t) => {
  const fixtureRoot = await mkdtemp(
    path.join(os.tmpdir(), "koyori-read-only-git-"),
  );
  t.after(async () => rm(fixtureRoot, { recursive: true, force: true }));
  const { spawnSync } = await import("node:child_process");
  const git = (args) => {
    const probe = spawnSync("git", args, {
      cwd: fixtureRoot,
      encoding: "utf8",
    });
    assert.equal(probe.status, 0, probe.stderr || probe.stdout);
  };
  git(["init"]);
  git(["config", "user.email", "readonly@example.invalid"]);
  git(["config", "user.name", "Read Only"]);
  await writeFile(path.join(fixtureRoot, "tracked.txt"), "committed\n");
  git(["add", "tracked.txt"]);
  git(["commit", "-m", "initial"]);
  const indexPath = path.join(fixtureRoot, ".git", "index");
  const beforeBytes = await readFile(indexPath);
  const beforeInfo = await stat(indexPath);
  captureWorkingTreeEvidence(fixtureRoot);
  const afterBytes = await readFile(indexPath);
  const afterInfo = await stat(indexPath);
  assert.deepEqual(afterBytes, beforeBytes);
  assert.equal(afterInfo.mtimeMs, beforeInfo.mtimeMs);
});

test("source fingerprint rejects a symlinked source root", async (t) => {
  const fixtureRoot = await mkdtemp(
    path.join(os.tmpdir(), "koyori-source-root-"),
  );
  t.after(async () => rm(fixtureRoot, { recursive: true, force: true }));
  const externalServices = path.join(fixtureRoot, "external-services");
  await mkdir(externalServices, { recursive: true });
  await writeFile(
    path.join(externalServices, "service.go"),
    "package services\n",
  );
  try {
    await symlink(
      externalServices,
      path.join(fixtureRoot, "services"),
      process.platform === "win32" ? "junction" : "dir",
    );
  } catch (error) {
    if (error?.code === "EPERM") {
      t.skip("host cannot create a directory symlink or junction");
      return;
    }
    throw error;
  }

  await assert.rejects(
    collectSourceFingerprintFiles(fixtureRoot),
    /source fingerprint input cannot be a symlink: services/,
  );
});

test("source fingerprint must remain stable through artifact construction", () => {
  const expected = { files: ["main.go"], sha256: "source-a" };
  assert.doesNotThrow(() =>
    assertSourceFingerprintUnchanged(expected, { ...expected }, "build"),
  );
  assert.throws(
    () =>
      assertSourceFingerprintUnchanged(
        expected,
        { files: ["main.go"], sha256: "source-b" },
        "build",
      ),
    /source fingerprint changed during build/,
  );
  assert.throws(
    () =>
      assertSourceFingerprintUnchanged(
        expected,
        { files: ["main.go", "new.go"], sha256: "source-a" },
        "build",
      ),
    /source fingerprint file set changed during build/,
  );
});

test("skip-build requires a manifest-bound matching source and artifact", () => {
  const current = {
    sourceFingerprintScope: "build-inputs-v2",
    sourceFingerprintSha256: "source-a",
    sourceFingerprintFileCount: 3,
    artifact: "bin/koyori-ide.exe",
    sha256: "artifact-a",
    wailsCli: "v3.0.0-alpha2.111",
    buildTags: ["desktop", "production", "e2e"],
    commit: "abc123",
    workingTreeDirty: true,
    gitStatusSha256: "porcelain-a",
  };
  const previous = {
    ...current,
    artifact: "bin\\koyori-ide.exe",
    sourceFingerprintStableAfterBuild: true,
  };
  assert.doesNotThrow(() =>
    validateReusableArtifactEvidence(previous, current),
  );
  assert.throws(
    () =>
      validateReusableArtifactEvidence(
        { ...previous, sourceFingerprintStableAfterBuild: false },
        current,
      ),
    /was not verified after its build/,
  );
  assert.throws(
    () =>
      validateReusableArtifactEvidence(
        { ...previous, sourceFingerprintSha256: "source-b" },
        current,
      ),
    /source fingerprint does not match/,
  );
  assert.throws(
    () =>
      validateReusableArtifactEvidence(
        { ...previous, sha256: "artifact-b" },
        current,
      ),
    /artifact digest does not match/,
  );
  assert.throws(
    () =>
      validateReusableArtifactEvidence(
        { ...previous, gitStatusSha256: "porcelain-b" },
        current,
      ),
    /working-tree porcelain digest does not match/,
  );
});

function minimalWindowsX64PE(fill = 0x45) {
  const artifact = Buffer.alloc(1024 * 1024 + 1, fill);
  artifact.write("MZ", 0, "ascii");
  artifact.writeUInt32LE(0x80, 0x3c);
  artifact.write("PE\0\0", 0x80, "ascii");
  artifact.writeUInt16LE(0x8664, 0x84);
  artifact.writeUInt16LE(0x20b, 0x98);
  return artifact;
}

async function createWindowsPackagedEvidenceFixture(t) {
  const baseRoot = await mkdtemp(
    path.join(os.tmpdir(), "koyori-packaged-evidence-"),
  );
  t.after(async () => rm(baseRoot, { recursive: true, force: true }));
  const packagedEvidenceDir = path.join(
    baseRoot,
    "build",
    "e2e-evidence",
    "packaged-e2e",
  );
  const artifactPath = path.join(baseRoot, "bin", "koyori-ide.exe");
  await mkdir(packagedEvidenceDir, { recursive: true });
  await mkdir(path.dirname(artifactPath), { recursive: true });
  await writeFile(
    path.join(baseRoot, "go.mod"),
    "module example.invalid/packaged-evidence\n\ngo 1.25.0\n\nrequire (\n\tgithub.com/wailsapp/wails/v3 v3.0.0-alpha2.111\n)\n",
  );
  await writeFile(
    path.join(baseRoot, "main.go"),
    "package main\nfunc main() {}\n",
  );
  const artifact = minimalWindowsX64PE();
  await writeFile(artifactPath, artifact);
  const sourceFiles = await collectSourceFingerprintFiles(baseRoot);
  const runId = "a".repeat(64);
  const tree = captureWorkingTreeEvidence(baseRoot);
  const manifest = {
    ...createPackagedE2EManifest({
      runner: { platform: "win32", arch: "x64" },
      recordedAt: "2026-08-25T01:00:00.000Z",
      runId,
    }),
    artifact: "bin\\koyori-ide.exe",
    sha256: createHash("sha256").update(artifact).digest("hex"),
    sourceFingerprintSha256: await sourceFingerprint(baseRoot, sourceFiles),
    sourceFingerprintScope: "build-inputs-v2",
    sourceFingerprintFileCount: sourceFiles.length,
    sourceFingerprintStableAfterBuild: true,
    sourceFingerprintVerifiedAt: "2026-08-25T01:02:00.000Z",
    expectedWailsCli: "v3.0.0-alpha2.111",
    wailsCli: "v3.0.0-alpha2.111",
    commit: tree.commit,
    gitMetadataAvailable: tree.gitMetadataAvailable,
    workingTreeDirty: tree.workingTreeDirty,
    gitStatusSha256: tree.gitStatusSha256,
    fixtures: CORE_FIXTURE_IDS.map((id) => ({
      id,
      driverImplemented: true,
      status: "passed",
    })),
    phase: "complete",
    status: "passed",
    completedAt: "2026-08-25T01:03:00.000Z",
  };
  const writeManifest = () =>
    writeFile(
      path.join(packagedEvidenceDir, "manifest.json"),
      `${JSON.stringify(manifest, null, 2)}\n`,
    );
  await writeManifest();
  await writeFile(
    path.join(packagedEvidenceDir, "fresh-run.log"),
    [
      `[packaged-e2e] identity: runId=${runId}`,
      "[packaged-e2e] toolchain: wails3 v3.0.0-alpha2.111 matches go.mod (wails3)",
      "[packaged-e2e] build: wails3 build -tags desktop,production,e2e",
      `[packaged-e2e] evidence: sha256=${manifest.sha256}`,
      `[packaged-e2e] launch: runId=${runId} artifact pid=1001 endpoint=http://127.0.0.1:32001`,
      `[packaged-e2e] launch: runId=${runId} artifact pid=1002 endpoint=http://127.0.0.1:32002`,
      "[packaged-e2e] fixtures: 24/24 passed against the packaged artifact",
      "",
    ].join("\n"),
  );
  for (const index of [1, 2]) {
    await writeFile(
      path.join(packagedEvidenceDir, `launch-${index}.log`),
      `E2E automation listening on loopback address=127.0.0.1:${32000 + index} runId=${runId}\n[WebView2] Environment created successfully\n`,
    );
    await writeFile(
      path.join(packagedEvidenceDir, `handshake-${index}.json`),
      JSON.stringify({
        url: `http://127.0.0.1:${32000 + index}`,
        pid: 1000 + index,
        startedAt: `2026-08-25T01:0${index}:00.000Z`,
        runId,
      }),
    );
  }
  return {
    artifactPath,
    baseRoot,
    manifest,
    packagedEvidenceDir,
    writeManifest,
  };
}
function verifyWindowsFixture(fixture, overrides = {}) {
  return verifyWindowsPackagedE2EEvidence({
    ...fixture,
    hostPlatform: "win32",
    hostArch: "x64",
    ...overrides,
  });
}

test("accepts complete fresh Windows packaged evidence", async (t) => {
  const fixture = await createWindowsPackagedEvidenceFixture(t);
  const verified = await verifyWindowsPackagedE2EEvidence({
    ...fixture,
    hostPlatform: "win32",
    hostArch: "x64",
  });
  assert.equal(verified.fixtureCount, 24);
  assert.equal(verified.sha256, fixture.manifest.sha256);
  assert.equal(
    verified.sourceFingerprintSha256,
    fixture.manifest.sourceFingerprintSha256,
  );
});

test("rejects a non-Windows or non-x64 verifier host", async (t) => {
  const fixture = await createWindowsPackagedEvidenceFixture(t);
  await assert.rejects(
    verifyWindowsFixture(fixture, { hostPlatform: "linux" }),
    /verifier host is not Windows/,
  );
  await assert.rejects(
    verifyWindowsFixture(fixture, { hostArch: "arm64" }),
    /verifier host is not x64/,
  );
});

test("rejects non-PE, x86, and ARM64 packaged artifacts", async (t) => {
  const fixture = await createWindowsPackagedEvidenceFixture(t);
  await writeFile(fixture.artifactPath, Buffer.alloc(1024 * 1024 + 1, 0x45));
  await assert.rejects(verifyWindowsFixture(fixture), /not a DOS executable/);

  const x86 = minimalWindowsX64PE();
  x86.writeUInt16LE(0x14c, 0x84);
  await writeFile(fixture.artifactPath, x86);
  await assert.rejects(verifyWindowsFixture(fixture), /not an AMD64 PE/);

  const arm64 = minimalWindowsX64PE();
  arm64.writeUInt16LE(0xaa64, 0x84);
  await writeFile(fixture.artifactPath, arm64);
  await assert.rejects(verifyWindowsFixture(fixture), /not an AMD64 PE/);
});

test("requires retained Git availability to match the checkout", async (t) => {
  const fixture = await createWindowsPackagedEvidenceFixture(t);
  const { spawnSync } = await import("node:child_process");
  const git = (args) => {
    const probe = spawnSync("git", args, {
      cwd: fixture.baseRoot,
      encoding: "utf8",
    });
    assert.equal(probe.status, 0, probe.stderr || probe.stdout);
  };
  git(["init"]);
  git(["config", "user.email", "evidence@example.invalid"]);
  git(["config", "user.name", "Evidence"]);
  git(["add", "go.mod", "main.go"]);
  git(["commit", "-m", "initial"]);
  await assert.rejects(
    verifyWindowsFixture(fixture),
    /Git metadata availability changed/,
  );
});

test("accepts PowerShell UTF-16LE fresh-run output", async (t) => {
  const fixture = await createWindowsPackagedEvidenceFixture(t);
  const freshRunPath = path.join(fixture.packagedEvidenceDir, "fresh-run.log");
  const utf8 = await readFile(freshRunPath, "utf8");
  await writeFile(
    freshRunPath,
    Buffer.concat([Buffer.from([0xff, 0xfe]), Buffer.from(utf8, "utf16le")]),
  );
  await assert.doesNotReject(verifyWindowsFixture(fixture));
});

test("rejects a handshake from another packaged run", async (t) => {
  const fixture = await createWindowsPackagedEvidenceFixture(t);
  const handshakePath = path.join(
    fixture.packagedEvidenceDir,
    "handshake-2.json",
  );
  const handshake = JSON.parse(await readFile(handshakePath, "utf8"));
  handshake.runId = "b".repeat(64);
  await writeFile(handshakePath, JSON.stringify(handshake));
  await assert.rejects(
    verifyWindowsFixture(fixture),
    /handshake-2 does not match the packaged run identity/,
  );
});

test("rejects malformed or zero packaged run identities", async (t) => {
  for (const runId of ["old", "A".repeat(64), "0".repeat(64)]) {
    const fixture = await createWindowsPackagedEvidenceFixture(t);
    fixture.manifest.runId = runId;
    await fixture.writeManifest();
    await assert.rejects(
      verifyWindowsFixture(fixture),
      /packaged runId must be 256-bit lowercase hex|must not be all zeroes/,
    );
  }
});

test("rejects partial or reused Windows packaged evidence", async (t) => {
  const fixture = await createWindowsPackagedEvidenceFixture(t);
  fixture.manifest.status = "running";
  fixture.manifest.phase = "fixtures";
  await fixture.writeManifest();
  await assert.rejects(
    verifyWindowsFixture(fixture),
    /manifest status is not passed/,
  );

  fixture.manifest.status = "passed";
  fixture.manifest.phase = "complete";
  fixture.manifest.artifactReused = true;
  await fixture.writeManifest();
  await assert.rejects(verifyWindowsFixture(fixture), /requires a fresh build/);
});

test("rejects artifact or source drift after a Windows packaged run", async (t) => {
  const fixture = await createWindowsPackagedEvidenceFixture(t);
  await writeFile(fixture.artifactPath, minimalWindowsX64PE(0x46));
  await assert.rejects(
    verifyWindowsFixture(fixture),
    /artifact SHA-256 changed/,
  );

  await writeFile(fixture.artifactPath, minimalWindowsX64PE());
  await writeFile(
    path.join(fixture.baseRoot, "main.go"),
    "package main\nfunc main() { panic(1) }\n",
  );
  await assert.rejects(
    verifyWindowsFixture(fixture),
    /source fingerprint changed/,
  );
});

test("rejects stale or secret-bearing Windows packaged handshakes", async (t) => {
  const fixture = await createWindowsPackagedEvidenceFixture(t);
  const handshakePath = path.join(
    fixture.packagedEvidenceDir,
    "handshake-1.json",
  );
  await writeFile(
    handshakePath,
    JSON.stringify({
      url: "http://127.0.0.1:32001",
      pid: 1001,
      startedAt: "2026-08-24T01:01:00.000Z",
      runId: fixture.manifest.runId,
    }),
  );
  await assert.rejects(
    verifyWindowsFixture(fixture),
    /handshake-1 is outside the packaged run interval/,
  );

  await writeFile(
    handshakePath,
    JSON.stringify({
      url: "http://127.0.0.1:32001",
      pid: 1001,
      startedAt: "2026-08-25T01:01:00.000Z",
      runId: fixture.manifest.runId,
      token: "must-not-be-retained",
    }),
  );
  await assert.rejects(
    verifyWindowsFixture(fixture),
    /handshake-1 leaks its bearer token/,
  );
});

test("rejects incomplete or cross-run Windows packaged logs", async (t) => {
  const fixture = await createWindowsPackagedEvidenceFixture(t);
  const freshRunPath = path.join(fixture.packagedEvidenceDir, "fresh-run.log");
  await writeFile(
    freshRunPath,
    `[packaged-e2e] identity: runId=${fixture.manifest.runId}\nfresh build log was not retained\n`,
  );
  await assert.rejects(
    verifyWindowsFixture(fixture),
    /does not record the pinned Wails toolchain/,
  );

  const runId = fixture.manifest.runId;
  await writeFile(
    freshRunPath,
    [
      `[packaged-e2e] identity: runId=${runId}`,
      "[packaged-e2e] toolchain: wails3 v3.0.0-alpha2.111 matches go.mod (wails3)",
      "[packaged-e2e] build: wails3 build -tags desktop,production,e2e",
      `[packaged-e2e] evidence: sha256=${fixture.manifest.sha256}`,
      `[packaged-e2e] launch: runId=${runId} artifact pid=1001 endpoint=http://127.0.0.1:32001`,
      `[packaged-e2e] launch: runId=${runId} artifact pid=1002 endpoint=http://127.0.0.1:32002`,
      "[packaged-e2e] fixtures: 24/24 passed against the packaged artifact",
      "",
    ].join("\n"),
  );
  await writeFile(
    path.join(fixture.packagedEvidenceDir, "launch-2.log"),
    `E2E automation listening on loopback address=127.0.0.1:39999 runId=${runId}\n[WebView2] Environment created successfully\n`,
  );
  await assert.rejects(
    verifyWindowsFixture(fixture),
    /launch-2.log does not match its handshake endpoint/,
  );

  await writeFile(
    path.join(fixture.packagedEvidenceDir, "launch-2.log"),
    `E2E automation listening on loopback address=127.0.0.1:32001 runId=${runId}\n[WebView2] Environment created successfully\n`,
  );
  await writeFile(
    path.join(fixture.packagedEvidenceDir, "handshake-2.json"),
    JSON.stringify({
      url: "http://127.0.0.1:32001",
      pid: 1001,
      startedAt: "2026-08-25T01:01:00.000Z",
      runId,
    }),
  );
  await assert.rejects(
    verifyWindowsFixture(fixture),
    /handshake-2 reuses a prior launch identity/,
  );
});

test("rejects a symlinked Windows packaged manifest", async (t) => {
  const fixture = await createWindowsPackagedEvidenceFixture(t);
  const manifestPath = path.join(fixture.packagedEvidenceDir, "manifest.json");
  const externalManifestPath = path.join(
    fixture.baseRoot,
    "external-manifest.json",
  );
  await writeFile(
    externalManifestPath,
    `${JSON.stringify(fixture.manifest, null, 2)}\n`,
  );
  await rm(manifestPath);
  try {
    await symlink(externalManifestPath, manifestPath, "file");
  } catch (error) {
    if (error?.code === "EPERM") {
      t.skip("host cannot create a file symlink");
      return;
    }
    throw error;
  }
  await assert.rejects(
    verifyWindowsFixture(fixture),
    /manifest.json must not be a symlink/,
  );
});

test("rejects symlinked evidence and artifact parent paths", async (t) => {
  const fixture = await createWindowsPackagedEvidenceFixture(t);
  const externalBuild = path.join(fixture.baseRoot, "external-build");
  const buildPath = path.join(fixture.baseRoot, "build");
  await rename(buildPath, externalBuild);
  try {
    await symlink(
      externalBuild,
      buildPath,
      process.platform === "win32" ? "junction" : "dir",
    );
  } catch (error) {
    if (error?.code === "EPERM") {
      t.skip("host cannot create an evidence directory link");
      return;
    }
    throw error;
  }
  await assert.rejects(
    verifyWindowsFixture({
      ...fixture,
      packagedEvidenceDir: path.join(buildPath, "e2e-evidence", "packaged-e2e"),
    }),
    /packaged evidence path component must not be a symlink|resolves outside/,
  );

  await rm(buildPath, { recursive: true, force: true });
  await rename(externalBuild, buildPath);
  const externalBin = path.join(fixture.baseRoot, "external-bin");
  const binPath = path.join(fixture.baseRoot, "bin");
  await rename(binPath, externalBin);
  await symlink(
    externalBin,
    binPath,
    process.platform === "win32" ? "junction" : "dir",
  );
  await assert.rejects(
    verifyWindowsFixture(fixture),
    /packaged artifact path component must not be a symlink|resolves outside/,
  );
});

test("declares the complete packaged fixture contract", () => {
  assert.deepEqual(CORE_FIXTURE_IDS, [
    "open-workspace",
    "open-file",
    "edit",
    "save",
    "terminal-command",
    "terminal-exit-package",
    "terminal-reconnect-package",
    "lsp-hover-completion",
    "search-replace",
    "git-diff",
    "git-worktree-package",
    "git-rebase-package",
    "ai-diff-receipt-package",
    "ai-fail-cancel",
    "ai-request-context-package",
    "extension-api-g13-package",
    "monaco-editor-ready",
    "settings-concurrent-package",
    "debug-g14-package",
    "test-explorer-g15-package",
    "language-pack-g23-package",
    "language-pack-builtins-g23-package",
    "extension-host-g24-package",
    "kill-restart-recovery",
  ]);
  assert(
    FRONTEND_E2E_PROBE_MARKERS.includes("__koyoriIdeRunAgentToolRoundProbe"),
  );
  assert(
    FRONTEND_E2E_PROBE_MARKERS.includes(
      "__koyoriIdeRunConversationHandoffProbe",
    ),
  );
});

test("frontend dist precheck fails closed when the packaged Agent probe marker is missing", async (t) => {
  const dist = await mkdtemp(
    path.join(os.tmpdir(), "koyori-agent-probe-marker-"),
  );
  t.after(() => rm(dist, { recursive: true, force: true }));
  const assets = path.join(dist, "assets");
  await mkdir(assets, { recursive: true });
  await writeFile(
    path.join(assets, "index.js"),
    FRONTEND_E2E_PROBE_MARKERS.filter(
      (marker) => marker !== "__koyoriIdeRunAgentToolRoundProbe",
    ).join("\n"),
    "utf8",
  );

  await assert.rejects(
    verifyFrontendE2EProbeMarkers(dist),
    /__koyoriIdeRunAgentToolRoundProbe/,
  );
});

test("frontend dist precheck fails closed when the conversation handoff marker is missing", async (t) => {
  const dist = await mkdtemp(
    path.join(os.tmpdir(), "koyori-handoff-probe-marker-"),
  );
  t.after(() => rm(dist, { recursive: true, force: true }));
  const assets = path.join(dist, "assets");
  await mkdir(assets, { recursive: true });
  await writeFile(
    path.join(assets, "index.js"),
    FRONTEND_E2E_PROBE_MARKERS.filter(
      (marker) => marker !== "__koyoriIdeRunConversationHandoffProbe",
    ).join("\n"),
    "utf8",
  );

  await assert.rejects(
    verifyFrontendE2EProbeMarkers(dist),
    /__koyoriIdeRunConversationHandoffProbe/,
  );
});

test("records pre-build failures without retaining stale artifact evidence", () => {
  const manifest = createPackagedE2EManifest({
    expectedWailsCli: "v3.0.0-alpha2.111",
    runner: { platform: "win32", arch: "x64" },
    recordedAt: "2026-08-14T00:00:00.000Z",
  });
  assert.equal(manifest.status, "running");
  assert.equal(manifest.phase, "initializing");
  assert.equal(manifest.artifact, null);
  assert.equal(manifest.sha256, null);
  assert.equal(manifest.fixtures.length, 24);
  assert.ok(manifest.fixtures.every((entry) => entry.status === "not-run"));
  assert.equal(manifest.workingTreeDirty, null);
  assert.equal(manifest.gitStatusSha256, null);
  assert.equal(manifest.gitStatusPorcelain, null);

  const failed = markPackagedE2EManifestFailed(
    manifest,
    new Error("build: wails3 build exited 1"),
    "2026-08-14T00:01:00.000Z",
  );
  assert.equal(failed.status, "failed");
  assert.equal(failed.phase, "initializing");
  assert.equal(failed.failure, "build: wails3 build exited 1");
  assert.equal(failed.completedAt, "2026-08-14T00:01:00.000Z");
  assert.equal(failed.artifact, null);
  assert.ok(failed.fixtures.every((entry) => entry.status === "not-run"));
});

test("rejects out-of-order packaged fixture result patches", () => {
  const manifest = createPackagedE2EManifest();
  assert.throws(
    () =>
      packagedE2EFixtureResultPatch(manifest, {
        id: "open-file",
        status: "passed",
      }),
    /fixture result out of order: open-file/,
  );
});

test("persists running evidence before a build and failed evidence after it throws", async () => {
  const events = [];
  const manifest = createPackagedE2EManifest({
    expectedWailsCli: "v3.0.0-alpha2.111",
    runner: { platform: "win32", arch: "x64" },
    recordedAt: "2026-08-14T00:00:00.000Z",
  });

  await assert.rejects(
    runPackagedE2EManifestLifecycle({
      manifest,
      writeManifest: async (value) => {
        events.push(`write:${value.status}:${value.phase}`);
      },
      run: async ({ checkpoint }) => {
        await checkpoint({ phase: "wails-build" });
        events.push("build");
        throw new Error("build: wails3 build exited 1");
      },
      now: () => "2026-08-14T00:01:00.000Z",
    }),
    /build: wails3 build exited 1/,
  );

  assert.deepEqual(events, [
    "write:running:initializing",
    "write:running:wails-build",
    "build",
    "write:failed:wails-build",
  ]);
  assert.equal(manifest.status, "failed");
  assert.equal(manifest.completedAt, "2026-08-14T00:01:00.000Z");
  assert.equal(manifest.artifact, null);
  assert.ok(manifest.fixtures.every((entry) => entry.status === "not-run"));
});

test("rotates the one-time bearer token after every authenticated command", async () => {
  const authorizations = [];
  const tokens = ["token-2", "token-3"];
  const client = new PackagedE2EClient({
    url: "http://127.0.0.1:32123",
    token: "token-1",
    fetchImpl: async (_url, options) => {
      authorizations.push(options.headers.Authorization);
      return new Response(JSON.stringify({ ok: true, result: {} }), {
        status: 200,
        headers: { "X-Koyori-IDE-E2E-Token": tokens.shift() },
      });
    },
  });

  await client.command("first");
  await client.command("second");
  assert.deepEqual(authorizations, ["Bearer token-1", "Bearer token-2"]);
});

test("fails a timed-out command without retrying a possibly mutating request", async () => {
  let attempts = 0;
  const client = new PackagedE2EClient({
    url: "http://127.0.0.1:32123",
    token: "token-1",
    commandTimeoutMs: 20,
    fetchImpl: async (_url, options) => {
      attempts++;
      await new Promise((_resolve, reject) => {
        options.signal.addEventListener(
          "abort",
          () => reject(options.signal.reason),
          { once: true },
        );
      });
    },
  });

  await assert.rejects(
    client.command("mutating-command"),
    /timed out after 20ms/,
  );
  assert.equal(attempts, 1);
});

function createCoreFixtureHarness({
  extensionHostError = null,
  omitAgentToolRounds = false,
  omitConversationHandoff = false,
  agentToolRoundsOverrides = {},
  readAgentToolRoundOverrides = {},
  searchAgentToolRoundOverrides = {},
  writeApproveAgentToolRoundOverrides = {},
  writeRejectAgentToolRoundOverrides = {},
  runApproveAgentToolRoundOverrides = {},
  runRejectAgentToolRoundOverrides = {},
  conversationHandoffOverrides = {},
  onCommand = () => {},
} = {}) {
  const actions = [];
  const evidence = [];
  let currentContent = "package fixture\n";
  const firstClient = {
    command: async (action, payload) => {
      actions.push(`first:${action}`);
      onCommand(action);
      if (action === "recovery-scan") return { files: [], corrupt: [] };
      if (action === "open-file") return { content: currentContent };
      if (action === "edit") return { baselineHash: "baseline-hash" };
      if (action === "save") {
        currentContent = payload.content;
        return { saved: true };
      }
      if (action === "terminal-command") return { output: payload.expected };
      if (action === "terminal-exit-probe") {
        return {
          illegalShellRejected: true,
          resizeOk: true,
          exitEventReceived: true,
          exitCode: 7,
        };
      }
      if (action === "terminal-reconnect-probe") {
        return {
          exitObserved: true,
          exitCode: 7,
          reconnectButtonVisible: true,
          reconnectButtonLabel: "Reconnect terminal",
          sameSessionReused: true,
          outputAfterReconnect: true,
          ok: true,
        };
      }
      if (action === "lsp-hover-completion")
        return { completionCount: 1, hover: "fixture" };
      if (action === "search-replace") return { matches: 1, replacements: 1 };
      if (action === "git-diff") return { changed: true, diff: "fixture diff" };
      if (action === "git-worktree-probe") {
        return {
          repoInitialized: true,
          siblingCreated: true,
          siblingListed: true,
          outsideRejected: true,
        };
      }
      if (action === "git-rebase-probe") {
        return {
          todoLoaded: true,
          rebaseStarted: true,
          actionsApplied: true,
          rebaseCompleted: true,
          noRebaseInProgress: true,
          commitCount: 2,
        };
      }
      if (action === "ai-diff-receipt-probe") {
        return {
          committedOnce: true,
          transactionId: "tx-1",
          fileHashesRecorded: true,
          diskMatchesCommit: true,
          duplicateRejected: true,
          diskUnchangedOnReject: true,
        };
      }
      if (action === "ai-fail-cancel")
        return { sendFailed: true, streamStopped: true };
      if (action === "ai-request-context-probe") {
        return {
          systemPromptReachedProvider: true,
          planInSystemPrompt: true,
          personaInSystemPrompt: true,
          imageBlockReachedProvider: true,
          captured: true,
          ...(!omitAgentToolRounds
            ? {
                agentToolRounds: {
                  readAuto: {
                    ok: true,
                    round: "readAuto",
                    toolKind: "read",
                    approvalMode: "auto-approve",
                    expectedDecision: "approve",
                    outcome: "executed",
                    backendApprovalPolicyObserved: true,
                    backendCatalogPolicyObserved: true,
                    providerRequestCount: 2,
                    firstRequestOfferedTool: true,
                    firstRequestContainedUserTurn: true,
                    nativeToolCallObserved: true,
                    decisionObserved: true,
                    approvalObserved: true,
                    approvalPrecededExecution: true,
                    backendCapabilityExecutionObserved: true,
                    executionUsageObserved: true,
                    usageUnitId: "unit-packaged-agent-read",
                    usageSessionId: "chat-packaged-agent-read",
                    usageOperation: "read",
                    usageSuccess: true,
                    usagePending: false,
                    usageSessionMatchesRequest: true,
                    usageObservationMatchesResult: true,
                    observationSubmitted: true,
                    rejectionSubmitted: false,
                    nativeProtocolResultSubmitted: true,
                    backendObservation: `Read agent-tool-round.txt: ${payload.marker}`,
                    secondRequestContainedObservation: true,
                    secondRequestUsedNativeToolProtocol: true,
                    finalAssistantObserved: true,
                    finalAssistant: "PACKAGED_AGENT_READ_ROUND_COMPLETE",
                    toolCallId: "call_packaged_agent_read",
                    manualControlRequired: false,
                    manualControlClicked: false,
                    ...readAgentToolRoundOverrides,
                  },
                  searchAuto: {
                    ok: true,
                    round: "searchAuto",
                    toolKind: "search",
                    approvalMode: "auto-approve",
                    expectedDecision: "approve",
                    outcome: "executed",
                    backendApprovalPolicyObserved: true,
                    backendCatalogPolicyObserved: true,
                    providerRequestCount: 2,
                    firstRequestOfferedTool: true,
                    firstRequestContainedUserTurn: true,
                    nativeToolCallObserved: true,
                    decisionObserved: true,
                    approvalObserved: true,
                    approvalPrecededExecution: true,
                    backendCapabilityExecutionObserved: true,
                    executionUsageObserved: true,
                    usageUnitId: "unit-packaged-agent-search",
                    usageSessionId: "chat-packaged-agent-search",
                    usageOperation: "search",
                    usageSuccess: true,
                    usagePending: false,
                    usageSessionMatchesRequest: true,
                    usageObservationMatchesResult: true,
                    observationSubmitted: true,
                    rejectionSubmitted: false,
                    nativeProtocolResultSubmitted: true,
                    backendObservation: `Search result agent-tool-round.txt: ${payload.marker}`,
                    secondRequestContainedObservation: true,
                    secondRequestUsedNativeToolProtocol: true,
                    finalAssistantObserved: true,
                    finalAssistant: "PACKAGED_AGENT_SEARCH_ROUND_COMPLETE",
                    toolCallId: "call_packaged_agent_search",
                    manualControlRequired: false,
                    manualControlClicked: false,
                    ...searchAgentToolRoundOverrides,
                  },
                  writeManualApprove: {
                    ok: true,
                    round: "writeManualApprove",
                    toolKind: "write",
                    approvalMode: "ask",
                    expectedDecision: "approve",
                    outcome: "executed",
                    backendCatalogPolicyObserved: true,
                    providerRequestCount: 2,
                    firstRequestOfferedTool: true,
                    firstRequestContainedUserTurn: true,
                    nativeToolCallObserved: true,
                    decisionObserved: true,
                    approvalObserved: true,
                    approvalPrecededExecution: true,
                    backendCapabilityExecutionObserved: true,
                    executionUsageObserved: true,
                    usageUnitId: "unit-packaged-agent-write-approve",
                    usageSessionId: "chat-packaged-agent-write-approve",
                    usageOperation: "write",
                    usageSuccess: true,
                    usagePending: false,
                    usageSessionMatchesRequest: true,
                    usageObservationMatchesResult: true,
                    observationSubmitted: true,
                    rejectionSubmitted: false,
                    nativeProtocolResultSubmitted: true,
                    backendObservation:
                      "Wrote agent-write-approve-deadbeefcafe.txt",
                    secondRequestContainedObservation: true,
                    secondRequestUsedNativeToolProtocol: true,
                    finalAssistantObserved: true,
                    finalAssistant:
                      "PACKAGED_AGENT_WRITE_APPROVE_ROUND_COMPLETE",
                    toolCallId: "call_packaged_agent_write_approve",
                    manualControlRequired: true,
                    manualControlRendered: true,
                    manualControlClicked: true,
                    manualControlClickEventObserved: true,
                    manualControlWasEnabled: true,
                    manualControlAction: "approve",
                    manualControlCallId: "call_packaged_agent_write_approve",
                    manualControlKind: "write",
                    backendApprovalSource: "e2e-exact-native-approver",
                    backendNativeApprovalObserved: true,
                    backendNativeApprovalCallCount: 1,
                    backendNativeApprovalExpectedCalls: 1,
                    backendNativeApprovalDecision: true,
                    backendNativeApprovalSequence: 1,
                    beforeExists: false,
                    afterExists: true,
                    diskMatchesRequestedContent: true,
                    unrelatedWorkspaceUnchanged: true,
                    afterSha256: "a".repeat(64),
                    expectedContentSha256: "a".repeat(64),
                    approvedBytes: 64,
                    approvedPath: path.join(
                      payload.workspace,
                      "agent-write-approve-deadbeefcafe.txt",
                    ),
                    ...writeApproveAgentToolRoundOverrides,
                  },
                  writeManualReject: {
                    ok: true,
                    round: "writeManualReject",
                    toolKind: "write",
                    approvalMode: "ask",
                    expectedDecision: "reject",
                    outcome: "rejected",
                    backendCatalogPolicyObserved: true,
                    providerRequestCount: 2,
                    firstRequestOfferedTool: true,
                    firstRequestContainedUserTurn: true,
                    nativeToolCallObserved: true,
                    decisionObserved: true,
                    approvalObserved: false,
                    approvalPrecededExecution: false,
                    backendCapabilityExecutionObserved: false,
                    executionUsageObserved: false,
                    observationSubmitted: false,
                    rejectionSubmitted: true,
                    backendRejection: "User rejected the write action",
                    nativeProtocolResultSubmitted: true,
                    secondRequestContainedObservation: true,
                    secondRequestUsedNativeToolProtocol: true,
                    finalAssistantObserved: true,
                    finalAssistant:
                      "PACKAGED_AGENT_WRITE_REJECT_ROUND_COMPLETE",
                    toolCallId: "call_packaged_agent_write_reject",
                    manualControlRequired: true,
                    manualControlRendered: true,
                    manualControlClicked: true,
                    manualControlClickEventObserved: true,
                    manualControlWasEnabled: true,
                    manualControlAction: "reject",
                    manualControlCallId: "call_packaged_agent_write_reject",
                    manualControlKind: "write",
                    backendApprovalSource: "e2e-exact-native-approver",
                    backendNativeApprovalObserved: false,
                    backendNativeApprovalCallCount: 0,
                    backendNativeApprovalExpectedCalls: 0,
                    beforeExists: false,
                    afterExists: false,
                    diskUnchanged: true,
                    workspaceUnchanged: true,
                    ...writeRejectAgentToolRoundOverrides,
                  },
                  runManualApprove: {
                    ok: true,
                    round: "runManualApprove",
                    toolKind: "run",
                    approvalMode: "ask",
                    expectedDecision: "approve",
                    outcome: "executed",
                    backendCatalogPolicyObserved: true,
                    providerRequestCount: 2,
                    firstRequestOfferedTool: true,
                    firstRequestContainedUserTurn: true,
                    nativeToolCallObserved: true,
                    decisionObserved: true,
                    approvalObserved: true,
                    approvalPrecededExecution: true,
                    backendCapabilityExecutionObserved: true,
                    executionUsageObserved: true,
                    usageUnitId: "unit-packaged-agent-run-approve",
                    usageSessionId: "chat-packaged-agent-run-approve",
                    usageOperation: "run",
                    usageSuccess: true,
                    usagePending: false,
                    usageSessionMatchesRequest: true,
                    usageObservationMatchesResult: true,
                    observationSubmitted: true,
                    rejectionSubmitted: false,
                    backendObservation: `run output: ${payload.marker}`,
                    nativeProtocolResultSubmitted: true,
                    secondRequestContainedObservation: true,
                    secondRequestUsedNativeToolProtocol: true,
                    finalAssistantObserved: true,
                    finalAssistant: "PACKAGED_AGENT_RUN_APPROVE_ROUND_COMPLETE",
                    toolCallId: "call_packaged_agent_run_approve",
                    manualControlRequired: true,
                    manualControlRendered: true,
                    manualControlClicked: true,
                    manualControlClickEventObserved: true,
                    manualControlWasEnabled: true,
                    manualControlAction: "approve",
                    manualControlCallId: "call_packaged_agent_run_approve",
                    manualControlKind: "run",
                    backendApprovalSource: "e2e-exact-native-approver",
                    backendNativeApprovalObserved: true,
                    backendNativeApprovalCallCount: 1,
                    backendNativeApprovalExpectedCalls: 1,
                    backendNativeApprovalDecision: true,
                    backendNativeApprovalSequence: 1,
                    externalReceiptId: "external-receipt-packaged-run",
                    externalReceiptReversible: false,
                    externalCompensation: "not-needed",
                    processOutputObserved: true,
                    workspaceUnchanged: true,
                    approvedCommand:
                      "findstr.exe /L PACKAGED_AGENT_TOOL_OBSERVATION agent-tool-round.txt",
                    approvedCwd: payload.workspace,
                    approvedRisk: "elevated",
                    ...runApproveAgentToolRoundOverrides,
                  },
                  runManualReject: {
                    ok: true,
                    round: "runManualReject",
                    toolKind: "run",
                    approvalMode: "ask",
                    expectedDecision: "reject",
                    outcome: "rejected",
                    backendCatalogPolicyObserved: true,
                    providerRequestCount: 2,
                    firstRequestOfferedTool: true,
                    firstRequestContainedUserTurn: true,
                    nativeToolCallObserved: true,
                    decisionObserved: true,
                    approvalObserved: false,
                    approvalPrecededExecution: false,
                    backendCapabilityExecutionObserved: false,
                    executionUsageObserved: false,
                    observationSubmitted: false,
                    rejectionSubmitted: true,
                    backendRejection: "User rejected the run action",
                    nativeProtocolResultSubmitted: true,
                    secondRequestContainedObservation: true,
                    secondRequestUsedNativeToolProtocol: true,
                    finalAssistantObserved: true,
                    finalAssistant: "PACKAGED_AGENT_RUN_REJECT_ROUND_COMPLETE",
                    toolCallId: "call_packaged_agent_run_reject",
                    manualControlRequired: true,
                    manualControlRendered: true,
                    manualControlClicked: true,
                    manualControlClickEventObserved: true,
                    manualControlWasEnabled: true,
                    manualControlAction: "reject",
                    manualControlCallId: "call_packaged_agent_run_reject",
                    manualControlKind: "run",
                    backendApprovalSource: "e2e-exact-native-approver",
                    backendNativeApprovalObserved: false,
                    backendNativeApprovalCallCount: 0,
                    backendNativeApprovalExpectedCalls: 0,
                    processOutputObserved: false,
                    workspaceUnchanged: true,
                    ...runRejectAgentToolRoundOverrides,
                  },
                  workspaceUnchanged: true,
                  ...agentToolRoundsOverrides,
                },
              }
            : {}),
        };
      }
      if (action === "conversation-handoff-probe") {
        if (omitConversationHandoff) return undefined;
        return {
          ok: true,
          aiWindowOpen: true,
          aiWindowVisible: true,
          sameRendererInstance: true,
          sameNativeWindow: true,
          sameReceiverEpoch: true,
          rendererInstanceId: "handoff-renderer_packaged",
          mainRendererInstanceId: "handoff-renderer_main",
          receiverEpoch: "receiver_packaged_ai",
          firstConversationId: "conversation-packaged-a",
          firstRevision: 1,
          firstMarkerObserved: true,
          firstDOMMarkerObserved: true,
          firstActiveConversationMatches: true,
          firstMode: "chat",
          firstAcknowledged: true,
          secondConversationId: "conversation-packaged-b",
          secondRevision: 1,
          secondMarkerObserved: true,
          secondDOMMarkerObserved: true,
          secondActiveConversationMatches: true,
          secondMode: "agent",
          secondAcknowledged: true,
          windowStatsBefore: { aiWindowsCreated: 1, aiWindowsClosed: 0 },
          windowStatsAfter: { aiWindowsCreated: 1, aiWindowsClosed: 0 },
          ...conversationHandoffOverrides,
        };
      }
      if (action === "extension-api-g13-probe") {
        return {
          ok: true,
          saveAllNoBridgeFailsClosed: true,
          showInputBoxFailsClosed: true,
          showQuickPickFailsClosed: true,
          saveAllBridgeCallsRealSave: true,
          notificationRoutedToHost: true,
          outputChannelOperable: true,
          configurationBridged: true,
          treeViewRegistrationOperable: true,
        };
      }
      if (action === "g10-monaco-probe") {
        return {
          ok: true,
          editors: 1,
          monacoEditorDom: true,
          languageId: payload.path?.endsWith("index.ts") ? "typescript" : "go",
        };
      }
      if (action === "settings-concurrent") {
        return {
          windowAApplied: true,
          staleBRejected: true,
          bReloadSawA: true,
          bRetryApplied: true,
          bothFieldsPresent: true,
          finalTheme: "dark",
          finalFontSize: 16,
        };
      }
      if (action === "debug-g14-probe") {
        return {
          dlvLaunch: true,
          breakpointStop: true,
          nestedExpanded: true,
          singleStep: true,
          adapterReference: 1,
          nestedReference: 2,
          adapterId: "delve",
          sourcePackId: "org.koyori.ide.go",
          sourcePackVersion: "1.0.0",
        };
      }
      if (action === "test-explorer-g15-probe") {
        return {
          ok: true,
          passExitCode: 0,
          failExitCode: 1,
          passEntryStatus: "pass",
          failEntryStatus: "fail",
          passTreeStatus: "passed",
          failTreeStatus: "failed",
          passOutputVisible: true,
          failOutputVisible: true,
          runningCleared: true,
        };
      }
      if (action === "language-pack-g23-probe") {
        return {
          signedArchivesVerified: true,
          publisherTrustOnboarded: true,
          pythonRustInstalled: true,
          versionPinVerified: true,
          lspSourcesVerified: true,
          toolchainSourcesVerified: true,
          toolchainExecuted: true,
          pythonToolchain: { success: true, output: "Python compiled" },
          rustToolchain: { success: true, output: "Finished" },
          pythonLsp: "not-run: contract mock",
          rustLsp: "not-run: contract mock",
          disableEnableVerified: true,
          rollbackVerified: true,
          uninstallRestoreVerified: true,
        };
      }
      if (action === "language-pack-builtins-g23-probe") {
        return {
          goLspSource: true,
          goFormat: true,
          goBuild: true,
          goTest: true,
          typescriptLsp: { completionCount: 1, hover: "number" },
          typescriptFormat: true,
          typescriptBuild: true,
          typescriptTest: true,
          typescriptDebug: true,
          nativeDebugApprovalConsumed: true,
          nodeAdapterId: "node-inspector",
          nodeSourcePackId: "org.koyori.ide.typescript",
          nodeSourcePackVersion: "1.0.0",
          goFilePath: "/workspace/main.go",
          typescriptFilePath: "/workspace/g23-typescript/index.ts",
        };
      }
      if (action === "extension-host-g24-probe") {
        if (extensionHostError) throw extensionHostError;
        return {
          ok: true,
          initialDisabled: true,
          v1Activation: { version: "1.0.0" },
          v2Activation: { version: "2.0.0" },
          faultIsolation: {
            abiFallbackActivated: true,
            abiIncompatibleRejected: true,
            permissionDenied: true,
            forgedIgnored: true,
            crashRestarted: true,
            hangRestarted: true,
            messageRateRestarted: true,
            messageSizeRestarted: true,
            disabled: true,
          },
          uninstallVerification: { uninstalled: true },
          remainingInstalled: 0,
        };
      }
      return {};
    },
  };
  const restartedClient = {
    command: async (action) => {
      actions.push(`restarted:${action}`);
      if (action === "recovery-scan") {
        return {
          files: [
            { path: "/workspace/main.go", content: "dirty", status: "clean" },
          ],
        };
      }
      if (action === "ai-diff-receipt-recovery-probe") {
        return {
          receiptRecovered: true,
          transactionIdStable: true,
          fileHashesMatchDisk: true,
          receiptWorkspaceMatches: true,
          duplicateRejected: true,
          diskUnchangedOnReject: true,
        };
      }
      return {};
    },
  };

  return { actions, evidence, firstClient, restartedClient };
}

test("persists partial fixture results without masking a G24 product failure", async () => {
  const productError = new Error("extension-host-g24-probe failed (422)");
  const checkpointError = new Error("fixture checkpoint write failed");
  const { firstClient, restartedClient } = createCoreFixtureHarness({
    extensionHostError: productError,
  });
  const manifest = createPackagedE2EManifest({
    recordedAt: "2026-08-14T00:00:00.000Z",
  });
  const writes = [];
  let failG24Checkpoint = true;

  await assert.rejects(
    runPackagedE2EManifestLifecycle({
      manifest,
      writeManifest: async (value) => {
        writes.push(structuredClone(value));
        if (
          failG24Checkpoint &&
          value.status === "running" &&
          value.fixtures.at(-2).status === "failed"
        ) {
          failG24Checkpoint = false;
          throw checkpointError;
        }
      },
      run: async ({ checkpoint }) =>
        runCoreFixtures({
          client: firstClient,
          workspace: "/workspace",
          filePath: "/workspace/main.go",
          initialContent: "package fixture\n",
          savedContent: "package fixture\n\nfunc Saved() {}\n",
          dirtyContent: "dirty",
          restart: async () => restartedClient,
          onFixtureResult: async (entry) => {
            await checkpoint(packagedE2EFixtureResultPatch(manifest, entry));
          },
        }),
      now: () => "2026-08-14T00:01:00.000Z",
    }),
    (error) => error === productError,
  );

  assert.deepEqual(
    manifest.fixtures.slice(0, 22).map(({ id, status }) => ({ id, status })),
    CORE_FIXTURE_IDS.slice(0, 22).map((id) => ({ id, status: "passed" })),
  );
  assert.deepEqual(manifest.fixtures.at(-2), {
    id: "extension-host-g24-package",
    driverImplemented: true,
    status: "failed",
    failure: productError.message,
  });
  assert.deepEqual(manifest.fixtures.at(-1), {
    id: "kill-restart-recovery",
    driverImplemented: true,
    status: "not-run",
  });
  assert.equal(manifest.status, "failed");
  assert.equal(manifest.failure, productError.message);
  assert.equal(productError.fixtureProgressError, checkpointError);
  assert.deepEqual(writes.at(-1), manifest);
});

test("runs every fixture and restarts before checking recovery", async () => {
  const { actions, evidence, firstClient, restartedClient } =
    createCoreFixtureHarness();
  const fixtureResults = [];

  const completed = await runCoreFixtures({
    client: firstClient,
    workspace: "/workspace",
    filePath: "/workspace/main.go",
    initialContent: "package fixture\n",
    savedContent: "package fixture\n\nfunc Saved() {}\n",
    dirtyContent: "dirty",
    restart: async () => restartedClient,
    onEvidence: (entry) => evidence.push(entry),
    onFixtureResult: async (entry) => fixtureResults.push(entry),
  });

  assert.deepEqual(completed, CORE_FIXTURE_IDS);
  assert.deepEqual(
    fixtureResults.map(({ id, status }) => ({ id, status })),
    CORE_FIXTURE_IDS.map((id) => ({ id, status: "passed" })),
  );
  assert.deepEqual(actions, [
    "first:open-workspace",
    "first:recovery-scan",
    "first:open-file",
    "first:edit",
    "first:save",
    "first:open-file",
    "first:terminal-command",
    "first:terminal-exit-probe",
    "first:terminal-reconnect-probe",
    "first:lsp-hover-completion",
    "first:search-replace",
    "first:git-diff",
    "first:git-worktree-probe",
    "first:git-rebase-probe",
    "first:ai-diff-receipt-probe",
    "first:ai-fail-cancel",
    "first:conversation-handoff-probe",
    "first:ai-request-context-probe",
    "first:extension-api-g13-probe",
    "first:g10-monaco-probe",
    "first:settings-concurrent",
    "first:debug-g14-probe",
    "first:test-explorer-g15-probe",
    "first:language-pack-g23-probe",
    "first:language-pack-builtins-g23-probe",
    "first:g10-monaco-probe",
    "first:g10-monaco-probe",
    "first:extension-host-g24-probe",
    "first:create-file",
    "first:edit",
    "first:save",
    "first:open-file",
    "first:edit",
    "restarted:open-workspace",
    "restarted:ai-diff-receipt-recovery-probe",
    "restarted:recovery-scan",
  ]);
  assert.equal(evidence.length, 6);
  assert.equal(
    evidence.find((entry) => entry.conversationHandoff)?.conversationHandoff
      .sameRendererInstance,
    true,
  );
  assert.equal(
    evidence.find((entry) => entry.agentToolRounds)?.agentToolRounds.searchAuto
      .executionUsageObserved,
    true,
  );
  assert.equal(
    evidence.find((entry) => entry.g23LanguagePack)?.g23LanguagePack
      .rollbackVerified,
    true,
  );
  assert.equal(
    evidence.find((entry) => entry.g24ExtensionHost)?.g24ExtensionHost
      .editSaveAfterFaults,
    true,
  );
});

test("fails closed when packaged Agent tool-round evidence is missing", async () => {
  const { firstClient, restartedClient } = createCoreFixtureHarness({
    omitAgentToolRounds: true,
  });

  await assert.rejects(
    runCoreFixtures({
      client: firstClient,
      workspace: "/workspace",
      filePath: "/workspace/main.go",
      initialContent: "package fixture\n",
      savedContent: "package fixture\n\nfunc Saved() {}\n",
      dirtyContent: "dirty",
      restart: async () => restartedClient,
    }),
    /packaged Agent tool-round evidence is missing/,
  );
});

test("fails closed when packaged conversation handoff evidence is missing", async () => {
  const { firstClient, restartedClient } = createCoreFixtureHarness({
    omitConversationHandoff: true,
  });

  await assert.rejects(
    runCoreFixtures({
      client: firstClient,
      workspace: "/workspace",
      filePath: "/workspace/main.go",
      initialContent: "package fixture\n",
      savedContent: "package fixture\n\nfunc Saved() {}\n",
      dirtyContent: "dirty",
      restart: async () => restartedClient,
    }),
    /packaged conversation handoff evidence is missing/,
  );
});

for (const [name, overrides, expected] of [
  ["renderer remount", { sameRendererInstance: false }, /renderer remounted/],
  [
    "native window replacement",
    { sameNativeWindow: false },
    /native AI window changed/,
  ],
  ["receiver remount", { sameReceiverEpoch: false }, /receiver remounted/],
  [
    "reused conversation",
    { secondConversationId: "conversation-packaged-a" },
    /reused the first conversation/,
  ],
  [
    "missing exact ACK",
    { secondAcknowledged: false },
    /second handoff lacked an exact ACK/,
  ],
  ["missing DOM marker", { secondDOMMarkerObserved: false }, /did not render/],
  [
    "active identity mismatch",
    { secondActiveConversationMatches: false },
    /active conversation identity diverged/,
  ],
  [
    "stale native window count",
    { windowStatsAfter: { aiWindowsCreated: 2, aiWindowsClosed: 0 } },
    /creation count changed/,
  ],
]) {
  test(`fails closed on packaged conversation handoff with ${name}`, async () => {
    const { firstClient, restartedClient } = createCoreFixtureHarness({
      conversationHandoffOverrides: overrides,
    });

    await assert.rejects(
      runCoreFixtures({
        client: firstClient,
        workspace: "/workspace",
        filePath: "/workspace/main.go",
        initialContent: "package fixture\n",
        savedContent: "package fixture\n\nfunc Saved() {}\n",
        dirtyContent: "dirty",
        restart: async () => restartedClient,
      }),
      expected,
    );
  });
}

for (const [name, round, overrides, expected] of [
  ["empty usage UnitID", "read", { usageUnitId: "" }, /empty UnitID/],
  ["empty usage session", "read", { usageSessionId: "" }, /empty session ID/],
  [
    "wrong usage operation",
    "search",
    { usageOperation: "read" },
    /wrong operation/,
  ],
  ["wrong tool kind", "search", { toolKind: "read" }, /wrong tool kind/],
  [
    "wrong tool call ID",
    "search",
    { toolCallId: "call_packaged_agent_read" },
    /unexpected native search tool call identity/,
  ],
  [
    "wrong provider request count",
    "search",
    { providerRequestCount: 3 },
    /exactly two turns/,
  ],
  [
    "missing offered tool",
    "search",
    { firstRequestOfferedTool: false },
    /did not offer the search tool/,
  ],
  [
    "missing backend catalog policy",
    "search",
    { backendCatalogPolicyObserved: false },
    /backend catalog policy/,
  ],
  [
    "unsuccessful terminal usage",
    "read",
    { usageSuccess: false },
    /did not reach success/,
  ],
  [
    "pending terminal usage",
    "read",
    { usagePending: true },
    /remained pending/,
  ],
  [
    "usage from another session",
    "read",
    { usageSessionMatchesRequest: false },
    /different Agent session/,
  ],
  [
    "mismatched observation",
    "read",
    { usageObservationMatchesResult: false },
    /observation diverged/,
  ],
  [
    "missing structured tool result",
    "read",
    { nativeProtocolResultSubmitted: false },
    /structured native read tool result/,
  ],
  [
    "missing observation in second request",
    "search",
    { secondRequestContainedObservation: false },
    /did not contain the observation/,
  ],
  [
    "approval after execution",
    "read",
    { approvalPrecededExecution: false },
    /approval did not precede/,
  ],
  [
    "legacy second provider observation",
    "read",
    { secondRequestUsedNativeToolProtocol: false },
    /did not preserve the native tool-call\/result protocol/,
  ],
]) {
  test(`fails closed on packaged Agent evidence with ${name}`, async () => {
    const { firstClient, restartedClient } = createCoreFixtureHarness({
      ...(round === "read"
        ? { readAgentToolRoundOverrides: overrides }
        : { searchAgentToolRoundOverrides: overrides }),
    });

    await assert.rejects(
      runCoreFixtures({
        client: firstClient,
        workspace: "/workspace",
        filePath: "/workspace/main.go",
        initialContent: "package fixture\n",
        savedContent: "package fixture\n\nfunc Saved() {}\n",
        dirtyContent: "dirty",
        restart: async () => restartedClient,
      }),
      expected,
    );
  });
}

const manualRoundOverrideOption = Object.freeze({
  writeManualApprove: "writeApproveAgentToolRoundOverrides",
  writeManualReject: "writeRejectAgentToolRoundOverrides",
  runManualApprove: "runApproveAgentToolRoundOverrides",
  runManualReject: "runRejectAgentToolRoundOverrides",
});

for (const [name, round, overrides, expected] of [
  [
    "write approval component missing",
    "writeManualApprove",
    { manualControlRendered: false },
    /did not render the real approval component/,
  ],
  [
    "write approval control disabled",
    "writeManualApprove",
    { manualControlWasEnabled: false },
    /clicked a disabled manual control/,
  ],
  [
    "write approval action changed",
    "writeManualApprove",
    { manualControlAction: "reject" },
    /clicked the wrong manual action/,
  ],
  [
    "write backend approval skipped",
    "writeManualApprove",
    { backendNativeApprovalObserved: false },
    /did not reach backend native approval/,
  ],
  [
    "write backend approval repeated",
    "writeManualApprove",
    { backendNativeApprovalCallCount: 2 },
    /exactly once/,
  ],
  [
    "write disk result missing",
    "writeManualApprove",
    { afterExists: false },
    /target was not created/,
  ],
  [
    "write disk hash changed",
    "writeManualApprove",
    { afterSha256: "b".repeat(64) },
    /content hash diverged/,
  ],
  [
    "write rejection executed",
    "writeManualReject",
    { backendCapabilityExecutionObserved: true },
    /reached backend capability execution/,
  ],
  [
    "write rejection created usage",
    "writeManualReject",
    { executionUsageObserved: true },
    /created execution usage/,
  ],
  [
    "write rejection changed disk",
    "writeManualReject",
    { afterExists: true },
    /created its target/,
  ],
  [
    "run receipt missing",
    "runManualApprove",
    { externalReceiptId: "" },
    /no external receipt/,
  ],
  [
    "run receipt reversible",
    "runManualApprove",
    { externalReceiptReversible: true },
    /receipt was reversible/,
  ],
  [
    "run cwd changed",
    "runManualApprove",
    { approvedCwd: "/outside" },
    /cwd left the workspace/,
  ],
  [
    "run used shell wrapper",
    "runManualApprove",
    { approvedCommand: "cmd.exe /c findstr marker file" },
    /controlled direct executable|shell wrapper/,
  ],
  [
    "run rejection reached approver",
    "runManualReject",
    { backendNativeApprovalCallCount: 1 },
    /called backend approval after renderer rejection/,
  ],
  [
    "run rejection spawned process",
    "runManualReject",
    { processOutputObserved: true },
    /produced process output/,
  ],
  [
    "run rejection retained receipt",
    "runManualReject",
    { externalReceiptId: "unexpected" },
    /unexpected external receipt/,
  ],
]) {
  test(`fails closed on packaged manual Agent round with ${name}`, async () => {
    const option = manualRoundOverrideOption[round];
    const { firstClient, restartedClient } = createCoreFixtureHarness({
      [option]: overrides,
    });

    await assert.rejects(
      runCoreFixtures({
        client: firstClient,
        workspace: "/workspace",
        filePath: "/workspace/main.go",
        initialContent: "package fixture\n",
        savedContent: "package fixture\n\nfunc Saved() {}\n",
        dirtyContent: "dirty",
        restart: async () => restartedClient,
      }),
      expected,
    );
  });
}

for (const [name, overrides, expected] of [
  [
    "missing search round",
    { searchAuto: undefined },
    /packaged Agent searchAuto evidence is missing/,
  ],
  [
    "changed search workspace",
    { workspaceUnchanged: false },
    /search round modified its workspace fixture/,
  ],
]) {
  test(`fails closed on packaged Agent rounds with ${name}`, async () => {
    const { firstClient, restartedClient } = createCoreFixtureHarness({
      agentToolRoundsOverrides: overrides,
    });

    await assert.rejects(
      runCoreFixtures({
        client: firstClient,
        workspace: "/workspace",
        filePath: "/workspace/main.go",
        initialContent: "package fixture\n",
        savedContent: "package fixture\n\nfunc Saved() {}\n",
        dirtyContent: "dirty",
        restart: async () => restartedClient,
      }),
      expected,
    );
  });
}

test("records 22 passed fixtures, the G24 failure, and leaves recovery not-run", async () => {
  const productError = new Error("extension-host-g24-probe failed (422)");
  const checkpointError = new Error("failure checkpoint write failed");
  let openWorkspaceCheckpointResolved = false;
  const { actions, firstClient, restartedClient } = createCoreFixtureHarness({
    extensionHostError: productError,
    onCommand: (action) => {
      if (action === "open-file") {
        assert.equal(openWorkspaceCheckpointResolved, true);
      }
    },
  });
  const fixtureStates = new Map(CORE_FIXTURE_IDS.map((id) => [id, "not-run"]));
  const fixtureResults = [];

  const run = runCoreFixtures({
    client: firstClient,
    workspace: "/workspace",
    filePath: "/workspace/main.go",
    initialContent: "package fixture\n",
    savedContent: "package fixture\n\nfunc Saved() {}\n",
    dirtyContent: "dirty",
    restart: async () => {
      assert.fail("recovery fixture must not run after the G24 failure");
      return restartedClient;
    },
    onFixtureResult: async (entry) => {
      fixtureResults.push(entry);
      fixtureStates.set(entry.id, entry.status);
      if (entry.id === "open-workspace") {
        await new Promise((resolve) => setImmediate(resolve));
        openWorkspaceCheckpointResolved = true;
      }
      if (entry.status === "failed") throw checkpointError;
    },
  });
  await assert.rejects(run, (error) => error === productError);

  assert.deepEqual(
    fixtureResults.slice(0, 22).map(({ id, status }) => ({ id, status })),
    CORE_FIXTURE_IDS.slice(0, 22).map((id) => ({ id, status: "passed" })),
  );
  assert.deepEqual(fixtureResults.at(-1), {
    id: "extension-host-g24-package",
    status: "failed",
    failure: productError.message,
  });
  assert.equal(fixtureStates.get("extension-host-g24-package"), "failed");
  assert.equal(fixtureStates.get("kill-restart-recovery"), "not-run");
  assert.equal(productError.fixtureProgressError, checkpointError);
});

test("does not report a passed-fixture checkpoint error as a product failure", async () => {
  const checkpointError = new Error("passed checkpoint write failed");
  const { actions, firstClient, restartedClient } = createCoreFixtureHarness();
  const fixtureResults = [];

  await assert.rejects(
    runCoreFixtures({
      client: firstClient,
      workspace: "/workspace",
      filePath: "/workspace/main.go",
      initialContent: "package fixture\n",
      savedContent: "package fixture\n\nfunc Saved() {}\n",
      dirtyContent: "dirty",
      restart: async () => restartedClient,
      onFixtureResult: async (entry) => {
        fixtureResults.push(entry);
        throw checkpointError;
      },
    }),
    (error) => error === checkpointError,
  );

  assert.deepEqual(fixtureResults, [
    { id: "open-workspace", status: "passed" },
  ]);
  assert.deepEqual(actions, ["first:open-workspace", "first:recovery-scan"]);
});
