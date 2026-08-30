#!/usr/bin/env node

/**
 * Packaged desktop E2E harness.
 *
 * This is not the contract smoke. It builds and launches the Wails artifact
 * with the test-only `e2e` build tag, then drives the real Project, File,
 * Recovery, Terminal, and LSP service graph through a loopback endpoint that
 * does not exist in production builds.
 *
 * Real artifact execution remains unverified until this command succeeds on a
 * GUI runner. `--dry-run` validates source-level fixture coverage only.
 */

import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import {
  mkdir,
  lstat,
  mkdtemp,
  readFile,
  readdir,
  realpath,
  rm,
  stat,
  writeFile,
} from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

import {
  CORE_FIXTURE_IDS,
  PackagedE2EClient,
  runCoreFixtures,
} from "./packaged-e2e-driver.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const dryRun = process.argv.includes("--dry-run");
const verifyEvidence = process.argv.includes("--verify-evidence");
// KOYORI_IDE_E2E_SKIP_BUILD=1 reuses the existing artifact under bin/ instead
// of rebuilding the frontend and the Wails binary. This is a memory-safe
// re-run path used when the source tree already produced a matching artifact
// (e.g. after a crash interrupted the fixture phase) and avoids the peak
// memory of Go + Vite compilation on memory-constrained runners.
const skipBuild = process.env.KOYORI_IDE_E2E_SKIP_BUILD === "1";
const evidenceDir = path.join(root, "build", "e2e-evidence", "packaged-e2e");
const STARTUP_TIMEOUT_MS = 20_000;
const STARTUP_SETTLE_MS = 1_500;
const PROCESS_STOP_TIMEOUT_MS = 5_000;
const MAX_CAPTURED_LOG_BYTES = 4 * 1024 * 1024;

function log(stage, detail) {
  console.log(`[packaged-e2e] ${stage}: ${detail}`);
}

function fail(stage, detail) {
  throw new Error(`${stage}: ${detail}`);
}

async function pinnedWailsVersion(baseRoot = root) {
  const goMod = await readFile(path.join(baseRoot, "go.mod"), "utf8");
  const match = goMod.match(/^\s*github\.com\/wailsapp\/wails\/v3\s+(v\S+)/m);
  assert(match, "github.com/wailsapp/wails/v3 is not pinned in go.mod");
  return match[1];
}

function installedWailsVersion() {
  // P9-G10: allow a pinned Wails CLI path (KOYORI_IDE_PINNED_WAILS3) so the
  // evidence run is not hostage to whatever wails3 happens to be on PATH.
  const supplied = process.env.KOYORI_IDE_PINNED_WAILS3?.trim();
  const command = supplied || "wails3";
  const probe = spawnSync(command, ["version"], { encoding: "utf8" });
  if (probe.error || probe.status !== 0) return null;
  const version =
    `${probe.stdout}${probe.stderr}`.match(/v3\.\S+/)?.[0] ?? null;
  return version ? { command, version } : null;
}

function currentCommit(cwd = root) {
  const probe = spawnSync("git", ["--no-optional-locks", "rev-parse", "HEAD"], {
    cwd,
    encoding: "utf8",
    env: { ...process.env, GIT_OPTIONAL_LOCKS: "0" },
  });
  if (probe.error || probe.status !== 0) return null;
  return probe.stdout.trim();
}

function gitOutput(args, cwd = root) {
  const probe = spawnSync("git", ["--no-optional-locks", ...args], {
    cwd,
    encoding: "utf8",
    env: { ...process.env, GIT_OPTIONAL_LOCKS: "0" },
  });
  if (probe.error || probe.status !== 0) return null;
  return probe.stdout.replaceAll("\r\n", "\n").trimEnd();
}

async function assertSafeEvidencePath(baseRoot, targetPath, label) {
  const relative = path.relative(baseRoot, targetPath);
  assert(
    relative === "" ||
      (!path.isAbsolute(relative) &&
        relative !== ".." &&
        !relative.startsWith(`..${path.sep}`)),
    `${label} escapes the evidence root`,
  );
  let current = baseRoot;
  const components = relative === "" ? [] : relative.split(path.sep);
  for (const component of components) {
    current = path.join(current, component);
    const info = await lstat(current);
    assert(
      !info.isSymbolicLink(),
      `${label} path component must not be a symlink: ${path.relative(baseRoot, current)}`,
    );
  }
  const [baseRealPath, targetRealPath] = await Promise.all([
    realpath(baseRoot),
    realpath(targetPath),
  ]);
  assert(
    targetRealPath === baseRealPath ||
      targetRealPath.startsWith(`${baseRealPath}${path.sep}`),
    `${label} resolves outside the evidence root`,
  );
}

function decodeEvidenceText(data) {
  if (data.length >= 2 && data[0] === 0xff && data[1] === 0xfe) {
    return data.subarray(2).toString("utf16le");
  }
  if (data.length >= 2 && data[0] === 0xfe && data[1] === 0xff) {
    assert.equal(
      data.length % 2,
      0,
      "UTF-16BE evidence has an odd byte length",
    );
    const littleEndian = Buffer.allocUnsafe(data.length - 2);
    for (let index = 2; index < data.length; index += 2) {
      littleEndian[index - 2] = data[index + 1];
      littleEndian[index - 1] = data[index];
    }
    return littleEndian.toString("utf16le");
  }
  return data[0] === 0xef && data[1] === 0xbb && data[2] === 0xbf
    ? data.subarray(3).toString("utf8")
    : data.toString("utf8");
}

function assertWindowsX64PE(data) {
  assert(data.length >= 0x40, "packaged artifact is not a PE file");
  assert.equal(
    data.toString("ascii", 0, 2),
    "MZ",
    "packaged artifact is not a DOS executable",
  );
  const peOffset = data.readUInt32LE(0x3c);
  assert(
    peOffset + 26 <= data.length,
    "packaged artifact has a truncated PE header",
  );
  assert.equal(
    data.toString("ascii", peOffset, peOffset + 4),
    "PE\0\0",
    "packaged artifact has an invalid PE signature",
  );
  assert.equal(
    data.readUInt16LE(peOffset + 4),
    0x8664,
    "packaged artifact is not an AMD64 PE",
  );
  assert.equal(
    data.readUInt16LE(peOffset + 24),
    0x20b,
    "packaged artifact is not a PE32+ image",
  );
}

function assertPackagedRunId(runId) {
  assert.match(
    runId,
    /^[0-9a-f]{64}$/,
    "packaged runId must be 256-bit lowercase hex",
  );
  assert.notEqual(
    runId,
    "0".repeat(64),
    "packaged runId must not be all zeroes",
  );
}

// Dirty/untracked files are part of the evidence identity. A HEAD SHA alone
// cannot bind a working tree that still has local edits (P13-G05 / AC5).
export function captureWorkingTreeEvidence(cwd = root) {
  const commit = currentCommit(cwd);
  const statusPorcelain = gitOutput(["status", "--porcelain=v1", "-uall"], cwd);
  const gitMetadataAvailable = commit !== null && statusPorcelain !== null;
  const dirty = Boolean(
    gitMetadataAvailable &&
    statusPorcelain.split("\n").some((line) => line.length > 0),
  );
  return {
    commit,
    gitMetadataAvailable,
    workingTreeDirty: gitMetadataAvailable ? dirty : null,
    gitStatusPorcelain: gitMetadataAvailable ? statusPorcelain : null,
    gitStatusSha256: gitMetadataAvailable
      ? createHash("sha256").update(statusPorcelain).digest("hex")
      : null,
  };
}

async function sha256(filePath) {
  const hash = createHash("sha256");
  hash.update(await readFile(filePath));
  return hash.digest("hex");
}

export const FRONTEND_E2E_PROBE_MARKERS = Object.freeze([
  "__koyoriIdeRunG10MonacoProbe",
  "__koyoriIdeRunG13ExtensionApiProbe",
  "__koyoriIdeRunG15TestExplorerProbe",
  "__koyoriIdeRunTerminalReconnectProbe",
  "__koyoriIdeRunG24ExtensionHostProbe",
  "__koyoriIdeRunAgentToolRoundProbe",
  "__koyoriIdeRunConversationHandoffProbe",
]);

export async function verifyFrontendE2EProbeMarkers(
  distPath = path.join(root, "frontend", "dist"),
) {
  const assetsDir = path.join(distPath, "assets");
  let assetNames;
  try {
    assetNames = await readdir(assetsDir);
  } catch (error) {
    fail(
      "frontend-probes",
      `cannot read frontend dist assets: ${error.message}`,
    );
  }
  const javascript = (
    await Promise.all(
      assetNames
        .filter((name) => name.endsWith(".js"))
        .map((name) => readFile(path.join(assetsDir, name), "utf8")),
    )
  ).join("\n");
  const missing = FRONTEND_E2E_PROBE_MARKERS.filter(
    (marker) => !javascript.includes(marker),
  );
  if (missing.length > 0) {
    fail(
      "frontend-probes",
      `E2E renderer marker(s) missing from dist: ${missing.join(", ")}`,
    );
  }
  log(
    "frontend-probes",
    `verified ${FRONTEND_E2E_PROBE_MARKERS.length} E2E renderer markers in dist`,
  );
  return FRONTEND_E2E_PROBE_MARKERS;
}

async function buildPackagedFrontend() {
  const windows = process.platform === "win32";
  const command = windows ? "cmd.exe" : "npm";
  const args = windows
    ? ["/d", "/s", "/c", "npm.cmd run build"]
    : ["run", "build"];
  const result = spawnSync(command, args, {
    cwd: path.join(root, "frontend"),
    stdio: "inherit",
    env: { ...process.env, VITE_KOYORI_IDE_E2E_MONACO: "1" },
  });
  if (result.status !== 0) {
    fail(
      "frontend-build",
      `npm run build exited ${result.status ?? "null"}${result.error ? `: ${result.error.message}` : ""}`,
    );
  }
}

const SOURCE_FINGERPRINT_SCOPE = "build-inputs-v2";
const SOURCE_FINGERPRINT_DIRECTORIES = Object.freeze([
  "services",
  "internal",
  "frontend",
  "scripts",
  "build",
]);
const SOURCE_FINGERPRINT_EXCLUDED_PREFIXES = Object.freeze([
  "frontend/node_modules",
  "frontend/bindings",
  "frontend/dist",
  "frontend/coverage",
  "build/e2e-evidence",
  "build/android/.gradle",
  "build/android/gen",
  "build/ios/xcode/gen",
]);
const SOURCE_FINGERPRINT_EXCLUDED_SEGMENT =
  /(^|\/)(?:node_modules|dist|coverage|\.vite|e2e-evidence)(\/|$)/;
const SOURCE_FINGERPRINT_EXCLUDED_PATHS = Object.freeze([
  /^frontend\/\.(?:bindings-tmp|probe-bindings)-[^/]+(?:\/|$)/,
  /^build\/overlay_(?:windows|darwin|linux)\.json$/,
  /^build\/(?:android|ios)\/overlay\.json$/,
  /^build\/android\/.*\/build(?:\/|$)/,
  /^build\/g03-manual-marker$/,
  /^build\/.*\.test(?:\.exe)?$/i,
]);
const SOURCE_FINGERPRINT_ROOT_FILE =
  /(?:\.(?:go|mod|sum|json|ya?ml|toml|mjs|cjs|ts|html)|^VERSION$)/i;

function sourceFingerprintPathExcluded(relative) {
  return (
    SOURCE_FINGERPRINT_EXCLUDED_SEGMENT.test(relative) ||
    SOURCE_FINGERPRINT_EXCLUDED_PATHS.some((pattern) =>
      pattern.test(relative),
    ) ||
    SOURCE_FINGERPRINT_EXCLUDED_PREFIXES.some(
      (prefix) => relative === prefix || relative.startsWith(`${prefix}/`),
    )
  );
}

async function collectSourceFingerprintDirectory(baseRoot, relative, files) {
  if (sourceFingerprintPathExcluded(relative)) return;
  let directoryInfo;
  try {
    directoryInfo = await lstat(path.join(baseRoot, relative));
  } catch (error) {
    if (error?.code === "ENOENT") return;
    throw error;
  }
  if (directoryInfo.isSymbolicLink()) {
    throw new Error(
      `source fingerprint input cannot be a symlink: ${relative}`,
    );
  }
  if (!directoryInfo.isDirectory()) {
    throw new Error(
      `source fingerprint directory is not a directory: ${relative}`,
    );
  }
  let entries;
  try {
    entries = await readdir(path.join(baseRoot, relative), {
      withFileTypes: true,
    });
  } catch (error) {
    if (error?.code === "ENOENT") return;
    throw error;
  }
  entries.sort((left, right) => left.name.localeCompare(right.name, "en"));
  for (const entry of entries) {
    const child = path.posix.join(relative, entry.name);
    if (sourceFingerprintPathExcluded(child)) continue;
    if (entry.isSymbolicLink()) {
      throw new Error(`source fingerprint input cannot be a symlink: ${child}`);
    }
    if (entry.isDirectory()) {
      await collectSourceFingerprintDirectory(baseRoot, child, files);
    } else if (entry.isFile()) {
      files.push(child);
    }
  }
}

// Commit metadata alone cannot bind dirty or untracked build inputs. Enumerate
// the complete backend/frontend/build harness source surface instead of a
// hand-maintained file list so a newly added Agent adapter changes evidence.
export async function collectSourceFingerprintFiles(baseRoot = root) {
  const files = [];
  const rootEntries = await readdir(baseRoot, { withFileTypes: true });
  for (const entry of rootEntries) {
    if (
      entry.isSymbolicLink() &&
      SOURCE_FINGERPRINT_ROOT_FILE.test(entry.name)
    ) {
      throw new Error(
        `source fingerprint input cannot be a symlink: ${entry.name}`,
      );
    }
    if (entry.isFile() && SOURCE_FINGERPRINT_ROOT_FILE.test(entry.name)) {
      files.push(entry.name);
    }
  }
  for (const relative of SOURCE_FINGERPRINT_DIRECTORIES) {
    await collectSourceFingerprintDirectory(baseRoot, relative, files);
  }
  files.sort();
  if (files.length === 0) {
    throw new Error("source fingerprint input set is empty");
  }
  return files;
}

export async function sourceFingerprint(baseRoot = root, discoveredFiles) {
  const files =
    discoveredFiles ?? (await collectSourceFingerprintFiles(baseRoot));
  const hash = createHash("sha256");
  for (const relative of files) {
    const digest = await sha256(path.join(baseRoot, relative));
    hash.update(relative);
    hash.update("\0");
    hash.update(digest);
    hash.update("\n");
  }
  return hash.digest("hex");
}

async function captureSourceFingerprint(baseRoot = root) {
  const files = await collectSourceFingerprintFiles(baseRoot);
  return {
    files,
    sha256: await sourceFingerprint(baseRoot, files),
  };
}

export function assertSourceFingerprintUnchanged(expected, actual, stage) {
  const expectedFiles = expected?.files ?? [];
  const actualFiles = actual?.files ?? [];
  if (
    expectedFiles.length !== actualFiles.length ||
    expectedFiles.some((relative, index) => relative !== actualFiles[index])
  ) {
    throw new Error(`source fingerprint file set changed during ${stage}`);
  }
  if (expected?.sha256 !== actual?.sha256) {
    throw new Error(`source fingerprint changed during ${stage}`);
  }
}

function normalizedArtifactPath(value) {
  return typeof value === "string" ? value.replaceAll("\\", "/") : value;
}

export function validateReusableArtifactEvidence(previous, current) {
  if (!previous || typeof previous !== "object") {
    throw new Error("skip-build has no prior packaged manifest to validate");
  }
  if (previous.sourceFingerprintStableAfterBuild !== true) {
    throw new Error(
      "skip-build manifest source was not verified after its build",
    );
  }
  if (
    previous.sourceFingerprintScope !== current.sourceFingerprintScope ||
    previous.sourceFingerprintSha256 !== current.sourceFingerprintSha256 ||
    previous.sourceFingerprintFileCount !== current.sourceFingerprintFileCount
  ) {
    throw new Error(
      "skip-build source fingerprint does not match prior artifact evidence",
    );
  }
  if (
    normalizedArtifactPath(previous.artifact) !==
    normalizedArtifactPath(current.artifact)
  ) {
    throw new Error(
      "skip-build artifact path does not match prior artifact evidence",
    );
  }
  if (previous.sha256 !== current.sha256) {
    throw new Error(
      "skip-build artifact digest does not match prior artifact evidence",
    );
  }
  if (previous.wailsCli !== current.wailsCli) {
    throw new Error(
      "skip-build Wails toolchain does not match prior artifact evidence",
    );
  }
  if (
    JSON.stringify(previous.buildTags) !== JSON.stringify(current.buildTags)
  ) {
    throw new Error(
      "skip-build build tags do not match prior artifact evidence",
    );
  }
  if (previous.commit !== current.commit) {
    throw new Error(
      "skip-build HEAD commit does not match prior artifact evidence",
    );
  }
  if (previous.workingTreeDirty !== current.workingTreeDirty) {
    throw new Error(
      "skip-build working-tree dirty flag does not match prior artifact evidence",
    );
  }
  if (previous.gitStatusSha256 !== current.gitStatusSha256) {
    throw new Error(
      "skip-build working-tree porcelain digest does not match prior artifact evidence",
    );
  }
}

function resolvedEvidenceArtifact(baseRoot, value) {
  assert.equal(
    normalizedArtifactPath(value),
    "bin/koyori-ide.exe",
    "qualified Windows evidence must use bin/koyori-ide.exe",
  );
  return path.join(baseRoot, "bin", "koyori-ide.exe");
}

function evidenceTimestamp(value, field) {
  assert.equal(typeof value, "string", `${field} must be an ISO timestamp`);
  const milliseconds = Date.parse(value);
  assert(Number.isFinite(milliseconds), `${field} must be an ISO timestamp`);
  return milliseconds;
}

async function readRequiredEvidenceFile(filePath, label) {
  const info = await lstat(filePath);
  // P19 CI 修复：符号链接在 lstat 视图里本来就不是常规文件——先判符号链接，
  // 让 symlinked-manifest 用例得到预期的 "must not be a symlink"（原先先
  // 命中 "is not a file"，拒绝语义相同但与驱动源契约测试的期望不符）。
  assert(!info.isSymbolicLink(), `${label} must not be a symlink`);
  assert(info.isFile(), `${label} is not a file`);
  assert(info.size > 0, `${label} is empty`);
  return decodeEvidenceText(await readFile(filePath));
}

async function assertArtifactPath(baseRoot, artifactPath) {
  await assertSafeEvidencePath(
    baseRoot,
    path.dirname(artifactPath),
    "packaged artifact",
  );
  await assertSafeEvidencePath(baseRoot, artifactPath, "packaged artifact");
}

export async function verifyWindowsPackagedE2EEvidence({
  baseRoot = root,
  packagedEvidenceDir = path.join(
    baseRoot,
    "build",
    "e2e-evidence",
    "packaged-e2e",
  ),
  hostPlatform = process.platform,
  hostArch = process.arch,
} = {}) {
  const baseRootInfo = await lstat(baseRoot);
  assert(
    baseRootInfo.isDirectory(),
    "packaged evidence base is not a directory",
  );
  assert(
    !baseRootInfo.isSymbolicLink(),
    "packaged evidence base must not be a symlink",
  );
  await assertSafeEvidencePath(
    baseRoot,
    packagedEvidenceDir,
    "packaged evidence",
  );
  assert.equal(hostPlatform, "win32", "verifier host is not Windows");
  assert.equal(hostArch, "x64", "verifier host is not x64");
  const evidenceDirectoryInfo = await lstat(packagedEvidenceDir);
  assert(
    evidenceDirectoryInfo.isDirectory(),
    "packaged evidence path is not a directory",
  );
  assert(
    !evidenceDirectoryInfo.isSymbolicLink(),
    "packaged evidence directory must not be a symlink",
  );
  const manifestPath = path.join(packagedEvidenceDir, "manifest.json");
  const manifest = JSON.parse(
    await readRequiredEvidenceFile(manifestPath, "manifest.json"),
  );
  assert(
    manifest && typeof manifest === "object",
    "packaged manifest is missing",
  );
  assertPackagedRunId(manifest.runId);
  assert.equal(
    manifest.status,
    "passed",
    "packaged manifest status is not passed",
  );
  assert.equal(
    manifest.phase,
    "complete",
    "packaged manifest phase is not complete",
  );
  assert.equal(
    manifest.failure,
    undefined,
    "passed manifest retains a failure",
  );
  assert.equal(
    manifest.runner?.platform,
    "win32",
    "packaged runner is not Windows",
  );
  assert.equal(manifest.runner?.arch, "x64", "packaged runner is not x64");
  assert.equal(
    manifest.artifactReused,
    false,
    "qualified evidence requires a fresh build",
  );
  assert.equal(
    manifest.sourceFingerprintStableAfterBuild,
    true,
    "source fingerprint was not verified after the build",
  );
  assert.equal(
    manifest.sourceFingerprintScope,
    SOURCE_FINGERPRINT_SCOPE,
    "source fingerprint scope is not current",
  );
  assert.deepEqual(
    manifest.buildTags,
    ["desktop", "production", "e2e"],
    "packaged build tags are not the qualified set",
  );

  const pinned = await pinnedWailsVersion(baseRoot);
  assert.equal(
    manifest.expectedWailsCli,
    pinned,
    "expected Wails CLI does not match go.mod",
  );
  assert.equal(
    manifest.wailsCli,
    pinned,
    "actual Wails CLI does not match go.mod",
  );

  assert.deepEqual(
    manifest.fixtures?.map((entry) => entry.id),
    CORE_FIXTURE_IDS,
    "packaged fixture identity or order is incomplete",
  );
  assert(
    manifest.fixtures.every(
      (entry) => entry.driverImplemented === true && entry.status === "passed",
    ),
    "not every packaged fixture passed",
  );

  const recordedAt = evidenceTimestamp(manifest.recordedAt, "recordedAt");
  const sourceVerifiedAt = evidenceTimestamp(
    manifest.sourceFingerprintVerifiedAt,
    "sourceFingerprintVerifiedAt",
  );
  const completedAt = evidenceTimestamp(manifest.completedAt, "completedAt");
  const artifactPath = resolvedEvidenceArtifact(baseRoot, manifest.artifact);
  await assertArtifactPath(baseRoot, artifactPath);
  const artifactInfo = await lstat(artifactPath);
  assert(artifactInfo.isFile(), "packaged artifact is not a file");
  assert(
    !artifactInfo.isSymbolicLink(),
    "packaged artifact must not be a symlink",
  );
  assert(
    artifactInfo.size > 1024 * 1024,
    "packaged artifact is implausibly small",
  );
  const artifactBytes = await readFile(artifactPath);
  assertWindowsX64PE(artifactBytes);
  assert.equal(
    createHash("sha256").update(artifactBytes).digest("hex"),
    manifest.sha256,
    "packaged artifact SHA-256 changed",
  );

  const sourceFiles = await collectSourceFingerprintFiles(baseRoot);
  assert.equal(
    sourceFiles.length,
    manifest.sourceFingerprintFileCount,
    "source fingerprint file count changed",
  );
  assert.equal(
    await sourceFingerprint(baseRoot, sourceFiles),
    manifest.sourceFingerprintSha256,
    "source fingerprint changed after the packaged run",
  );

  const tree = captureWorkingTreeEvidence(baseRoot);
  assert.equal(
    tree.gitMetadataAvailable,
    manifest.gitMetadataAvailable,
    "Git metadata availability changed after the packaged run",
  );
  if (tree.gitMetadataAvailable) {
    assert.equal(
      tree.commit,
      manifest.commit,
      "HEAD changed after the packaged run",
    );
    assert.equal(
      tree.workingTreeDirty,
      manifest.workingTreeDirty,
      "working-tree dirty state changed after the packaged run",
    );
    assert.equal(
      tree.gitStatusSha256,
      manifest.gitStatusSha256,
      "working-tree porcelain changed after the packaged run",
    );
  } else {
    assert.equal(
      manifest.commit,
      null,
      "manifest has a commit without Git metadata",
    );
    assert.equal(
      manifest.gitStatusSha256,
      null,
      "manifest has a porcelain digest without Git metadata",
    );
  }

  const freshRunLog = await readRequiredEvidenceFile(
    path.join(packagedEvidenceDir, "fresh-run.log"),
    "fresh-run.log",
  );
  assert(
    freshRunLog.includes(`[packaged-e2e] identity: runId=${manifest.runId}`),
    "fresh-run.log does not record the packaged run identity",
  );
  assert(
    freshRunLog.includes(
      `[packaged-e2e] toolchain: wails3 ${pinned} matches go.mod`,
    ),
    "fresh-run.log does not record the pinned Wails toolchain",
  );
  assert(
    freshRunLog.includes(
      "[packaged-e2e] build: wails3 build -tags desktop,production,e2e",
    ),
    "fresh-run.log does not record a fresh Wails build",
  );
  assert(
    freshRunLog.includes(`[packaged-e2e] evidence: sha256=${manifest.sha256}`),
    "fresh-run.log does not bind the artifact SHA-256",
  );
  assert(
    freshRunLog.includes(
      `[packaged-e2e] fixtures: ${CORE_FIXTURE_IDS.length}/${CORE_FIXTURE_IDS.length} passed against the packaged artifact`,
    ),
    "fresh-run.log does not record complete fixtures",
  );
  const handshakeIdentities = new Set();
  let previousHandshakeStartedAt = recordedAt;

  for (const index of [1, 2]) {
    const launch = await readRequiredEvidenceFile(
      path.join(packagedEvidenceDir, `launch-${index}.log`),
      `launch-${index}.log`,
    );
    assert.match(launch, /E2E automation listening on loopback/);
    assert.match(launch, /Environment created successfully/);
    assert(
      launch.includes(`runId=${manifest.runId}`),
      `launch-${index}.log does not bind the packaged run identity`,
    );
    const handshakeText = await readRequiredEvidenceFile(
      path.join(packagedEvidenceDir, `handshake-${index}.json`),
      `handshake-${index}.json`,
    );
    const handshake = JSON.parse(handshakeText);
    assert.equal(
      handshake.runId,
      manifest.runId,
      `handshake-${index} does not match the packaged run identity`,
    );
    assert.match(handshake.url, /^http:\/\/127\.0\.0\.1:\d+$/);
    assert(Number.isSafeInteger(handshake.pid) && handshake.pid > 0);
    const launchAddress = new URL(handshake.url).host;
    assert(
      launch.includes(`address=${launchAddress}`),
      `launch-${index}.log does not match its handshake endpoint`,
    );
    const handshakeStartedAt = evidenceTimestamp(
      handshake.startedAt,
      `handshake-${index}.startedAt`,
    );
    assert(
      recordedAt <= handshakeStartedAt && handshakeStartedAt <= completedAt,
      `handshake-${index} is outside the packaged run interval`,
    );
    assert(
      previousHandshakeStartedAt <= handshakeStartedAt,
      `handshake-${index} predates the previous launch`,
    );
    previousHandshakeStartedAt = handshakeStartedAt;
    const handshakeIdentity = `${handshake.pid}\0${handshake.url}`;
    assert(
      !handshakeIdentities.has(handshakeIdentity),
      `handshake-${index} reuses a prior launch identity`,
    );
    handshakeIdentities.add(handshakeIdentity);
    assert(
      freshRunLog.includes(
        `[packaged-e2e] launch: runId=${manifest.runId} artifact pid=${handshake.pid} endpoint=${handshake.url}`,
      ),
      `fresh-run.log does not bind launch-${index}`,
    );
    assert.equal(
      handshake.token,
      undefined,
      `handshake-${index} leaks its bearer token`,
    );
  }

  if (manifest.screenshot !== null) {
    assert.equal(typeof manifest.screenshot?.file, "string");
    assert.equal(
      path.basename(manifest.screenshot.file),
      manifest.screenshot.file,
      "packaged screenshot path must be a basename",
    );
    const screenshotInfo = await lstat(
      path.join(packagedEvidenceDir, manifest.screenshot.file),
    );
    assert(screenshotInfo.isFile(), "packaged screenshot is not a file");
    assert(
      !screenshotInfo.isSymbolicLink(),
      "packaged screenshot must not be a symlink",
    );
    assert.equal(
      screenshotInfo.size,
      manifest.screenshot.bytes,
      "screenshot byte count changed",
    );
  }

  return {
    artifact: normalizedArtifactPath(manifest.artifact),
    sha256: manifest.sha256,
    sourceFingerprintSha256: manifest.sourceFingerprintSha256,
    fixtureCount: manifest.fixtures.length,
    completedAt: manifest.completedAt,
  };
}

async function readPackagedE2EManifest(manifestPath) {
  try {
    return JSON.parse(await readFile(manifestPath, "utf8"));
  } catch (error) {
    if (error?.code === "ENOENT") return null;
    throw error;
  }
}

async function locateArtifact() {
  // P9-G10: prefer the canonical artifact name. Size-sorted selection can
  // pick up stale test binaries that lack the e2e build tag.
  const canonical =
    process.platform === "win32" ? "koyori-ide.exe" : "koyori-ide";
  const canonicalPath = path.join(root, "bin", canonical);
  try {
    const info = await stat(canonicalPath);
    if (info.isFile() && info.size > 1024 * 1024) return canonicalPath;
  } catch {
    // fall through to a size-sorted scan
  }
  const binDir = path.join(root, "bin");
  let entries;
  try {
    entries = await readdir(binDir, { withFileTypes: true });
  } catch {
    return null;
  }
  const candidates = [];
  for (const entry of entries) {
    if (!entry.isFile()) continue;
    const fullPath = path.join(binDir, entry.name);
    const info = await stat(fullPath);
    if (info.size > 1024 * 1024) candidates.push({ fullPath, size: info.size });
  }
  candidates.sort((left, right) => right.size - left.size);
  return candidates[0]?.fullPath ?? null;
}

function delay(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

async function waitForJSON(filePath, child) {
  const deadline = Date.now() + STARTUP_TIMEOUT_MS;
  while (Date.now() < deadline) {
    if (child.e2eSpawnError) {
      fail("launch", `artifact spawn failed: ${child.e2eSpawnError.message}`);
    }
    if (child.exitCode !== null || child.signalCode !== null) {
      fail(
        "launch",
        `artifact exited before handshake (code=${child.exitCode}, signal=${child.signalCode})`,
      );
    }
    try {
      return JSON.parse(await readFile(filePath, "utf8"));
    } catch {
      await delay(100);
    }
  }
  fail(
    "launch",
    `timed out waiting for handshake ${path.relative(root, filePath)}`,
  );
}

function appendBounded(current, chunk) {
  const next = current + chunk;
  return next.length <= MAX_CAPTURED_LOG_BYTES
    ? next
    : next.slice(next.length - MAX_CAPTURED_LOG_BYTES);
}

async function startVirtualDisplay() {
  if (process.platform !== "linux") {
    return { display: null, process: null, stop: async () => {} };
  }
  const display = process.env.KOYORI_IDE_E2E_DISPLAY || ":99";
  const logPath = path.join(evidenceDir, "xvfb.log");
  let output = "";
  const child = spawn(
    "Xvfb",
    [display, "-screen", "0", "1280x1024x24", "-nolisten", "tcp"],
    {
      cwd: root,
      stdio: ["ignore", "pipe", "pipe"],
    },
  );
  child.on("error", (error) => {
    child.e2eSpawnError = error;
  });
  child.stdout.on("data", (chunk) => {
    output = appendBounded(output, chunk.toString());
  });
  child.stderr.on("data", (chunk) => {
    output = appendBounded(output, chunk.toString());
  });
  await delay(750);
  if (child.e2eSpawnError) {
    await writeFile(logPath, output, "utf8");
    fail("xvfb", `Xvfb spawn failed: ${child.e2eSpawnError.message}`);
  }
  if (child.exitCode !== null || child.signalCode !== null) {
    await writeFile(logPath, output, "utf8");
    fail(
      "xvfb",
      `Xvfb exited during startup (code=${child.exitCode}, signal=${child.signalCode})`,
    );
  }
  return {
    display,
    process: child,
    stop: async () => {
      if (child.exitCode !== null || child.signalCode !== null) {
        await writeFile(logPath, output, "utf8");
        return;
      }
      child.kill("SIGTERM");
      await Promise.race([
        new Promise((resolve) => child.once("exit", resolve)),
        delay(PROCESS_STOP_TIMEOUT_MS),
      ]);
      if (child.exitCode === null && child.signalCode === null)
        child.kill("SIGKILL");
      await writeFile(logPath, output, "utf8");
    },
  };
}

function signalProcessGroup(child, signal) {
  if (!child || child.exitCode !== null || child.signalCode !== null) return;
  try {
    if (process.platform === "win32") child.kill(signal);
    else process.kill(-child.pid, signal);
  } catch (error) {
    if (error?.code !== "ESRCH") throw error;
  }
}

async function stopArtifact(launch, signal = "SIGTERM") {
  if (!launch || launch.stopped) return;
  launch.stopped = true;
  if (launch.child.exitCode !== null || launch.child.signalCode !== null) {
    await launch.flushLog();
    return;
  }
  signalProcessGroup(launch.child, signal);
  await Promise.race([
    new Promise((resolve) => launch.child.once("exit", resolve)),
    delay(PROCESS_STOP_TIMEOUT_MS),
  ]);
  if (launch.child.exitCode === null && launch.child.signalCode === null) {
    signalProcessGroup(launch.child, "SIGKILL");
  }
  await launch.flushLog();
}

// P9-G10: capture a real window screenshot of the packaged artifact on
// Windows (EnumWindows + CopyFromScreen via -EncodedCommand) and on Linux
// (Xvfb + import).
async function captureWindowScreenshot(pid, outputPath, display) {
  if (process.platform === "win32") {
    const encoded = Buffer.from(
      'Add-Type -AssemblyName System.Drawing\nAdd-Type @"\nusing System; using System.Runtime.InteropServices; using System.Text;\npublic struct G10Rect { public int Left; public int Top; public int Right; public int Bottom; }\npublic static class G10Cap { public delegate bool EnumWindowsProc(IntPtr h, IntPtr l);\n[DllImport("user32.dll")] public static extern bool EnumWindows(EnumWindowsProc c, IntPtr l);\n[DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr h);\n[DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr h, out uint p);\n[DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern int GetWindowText(IntPtr h, StringBuilder s, int m);\n[DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr h, out G10Rect r);\npublic static IntPtr Find(int pid) { IntPtr found=IntPtr.Zero; EnumWindows((h,l)=>{ uint owner; GetWindowThreadProcessId(h,out owner); if(owner!=(uint)pid||!IsWindowVisible(h))return true; var s=new StringBuilder(256);GetWindowText(h,s,s.Capacity);if(s.ToString().IndexOf("koyori-ide",StringComparison.OrdinalIgnoreCase)<0)return true; found=h; return false;},IntPtr.Zero); return found; } }\n"@\n$processId = [int]$env:G10_PID; $output = $env:G10_OUT\n$deadline=[DateTime]::UtcNow.AddSeconds(20); $handle=[IntPtr]::Zero\nwhile([DateTime]::UtcNow -lt $deadline){$handle=[G10Cap]::Find($processId);if($handle -ne [IntPtr]::Zero){break};Start-Sleep -Milliseconds 100}\nif($handle -eq [IntPtr]::Zero){throw "no visible koyori-ide window"}\n$rect=New-Object G10Rect\nif(-not [G10Cap]::GetWindowRect($handle,[ref]$rect)){throw "GetWindowRect failed"}\n$w=$rect.Right-$rect.Left;$h=$rect.Bottom-$rect.Top\nif($w -lt 200 -or $h -lt 150){throw "unexpected dimensions ${w}x${h}"}\n$bitmap=New-Object System.Drawing.Bitmap($w,$h)\n$graphics=[System.Drawing.Graphics]::FromImage($bitmap)\ntry{$graphics.CopyFromScreen($rect.Left,$rect.Top,0,0,$bitmap.Size);$bitmap.Save($output,[System.Drawing.Imaging.ImageFormat]::Png);($w.ToString() + \'x\' + $h.ToString())}finally{$graphics.Dispose();$bitmap.Dispose()}',
      "utf16le",
    ).toString("base64");
    const result = spawnSync(
      "powershell.exe",
      ["-NoProfile", "-NonInteractive", "-EncodedCommand", encoded],
      {
        encoding: "utf8",
        windowsHide: true,
        env: { ...process.env, G10_PID: String(pid), G10_OUT: outputPath },
      },
    );
    if (result.status !== 0) {
      await writeFile(
        path.join(evidenceDir, "screenshot-error.txt"),
        `${result.stderr || result.stdout}\n`,
        "utf8",
      );
      return null;
    }
    const info = await stat(outputPath);
    const dims = result.stdout
      .trim()
      .split(/\r?\n/)
      .at(-1)
      .split("x")
      .map(Number);
    const meta = { width: dims[0], height: dims[1] };
    return { file: path.basename(outputPath), bytes: info.size, ...meta };
  }
  if (display && process.platform === "linux") {
    const capture = spawnSync("import", ["-window", "root", outputPath], {
      encoding: "utf8",
      env: { ...process.env, DISPLAY: display },
    });
    if (capture.error || capture.status !== 0)
      throw new Error(
        `X11 capture failed: ${capture.stderr || capture.stdout}`,
      );
    const info = await stat(outputPath);
    return { file: path.basename(outputPath), bytes: info.size };
  }
  return null;
}

async function captureFailureScreenshot(display) {
  if (process.platform !== "linux" || !display) return;
  const screenshotPath = path.join(evidenceDir, "failure.png");
  const capture = spawnSync("import", ["-window", "root", screenshotPath], {
    encoding: "utf8",
    env: { ...process.env, DISPLAY: display },
  });
  if (capture.error || capture.status !== 0) {
    await writeFile(
      path.join(evidenceDir, "screenshot-error.txt"),
      `${capture.error?.message ?? ""}\n${capture.stdout ?? ""}\n${capture.stderr ?? ""}`,
      "utf8",
    );
  }
}

function runnerEvidence() {
  return {
    platform: process.platform,
    arch: process.arch,
    osRelease: os.release(),
    node: process.version,
    ci: process.env.CI === "true",
    githubActions: process.env.GITHUB_ACTIONS === "true",
    runnerOS: process.env.RUNNER_OS || null,
    runnerArch: process.env.RUNNER_ARCH || null,
    githubRunID: process.env.GITHUB_RUN_ID || null,
  };
}

async function createFixtureWorkspace() {
  const directory = await mkdtemp(
    path.join(os.tmpdir(), "koyori-ide-packaged-e2e-"),
  );
  const workspace = path.join(directory, "workspace");
  const configDir = path.join(directory, "config");
  await mkdir(workspace, { recursive: true });
  await mkdir(configDir, { recursive: true });
  const filePath = path.join(workspace, "main.go");
  const initialContent = "package fixture\n";
  const savedContent = [
    "package fixture",
    "",
    'import "fmt"',
    "",
    "func main() {",
    '	fmt.Println("ready")',
    "	fmt.Prin",
    "}",
    "",
  ].join("\n");
  const dirtyContent = `${savedContent}// unsaved after crash\n`;
  await writeFile(filePath, initialContent, "utf8");
  await writeFile(
    path.join(workspace, "go.mod"),
    "module fixture\n\ngo 1.25.0\n",
    "utf8",
  );
  // Keep the fixture out of the Go toolchain's ./... scan by placing
  // it in its own module subdirectory under the evidence folder.
  const fixtureDir = path.join(evidenceDir, "fixtures");
  await mkdir(fixtureDir, { recursive: true });
  await writeFile(
    path.join(fixtureDir, "go.mod"),
    "module koyori-e2e-fixture\n\ngo 1.25.0\n",
    "utf8",
  );
  await writeFile(path.join(fixtureDir, "main.go"), savedContent, "utf8");
  return {
    directory,
    workspace,
    configDir,
    filePath,
    initialContent,
    savedContent,
    dirtyContent,
  };
}

async function launchArtifact({ artifact, fixture, display, index, runId }) {
  const token = randomBytes(32).toString("hex");
  assert.match(runId, /^[0-9a-f]{64}$/);
  const handshakePath = path.join(evidenceDir, `handshake-${index}.json`);
  const logPath = path.join(evidenceDir, `launch-${index}.log`);
  await rm(handshakePath, { force: true });

  let output = "";
  await mkdir(path.join(fixture.configDir, `launch-${index}`), {
    recursive: true,
  });
  const child = spawn(artifact, [], {
    cwd: root,
    detached: process.platform !== "win32",
    env: {
      ...process.env,
      ...(display ? { DISPLAY: display } : {}),
      XDG_CONFIG_HOME: path.join(fixture.configDir, `launch-${index}`),
      // Isolate the instance lock and UserConfigDir-backed state (profiles,
      // settings path resolution) per launch, so a packaged artifact never
      // collides with a real user instance or another E2E launch.
      // APPDATA is shared across restarts of the same fixture so the recovery
      // journal (UserConfigDir-backed) survives a kill+restart cycle; the
      // instance lock serializes the single artifact process.
      APPDATA: path.join(fixture.configDir, "appdata"),
      KOYORI_IDE_E2E: "1",
      KOYORI_IDE_E2E_TOKEN: token,
      KOYORI_IDE_E2E_HANDSHAKE: handshakePath,
      KOYORI_IDE_E2E_RUN_ID: runId,
      KOYORI_IDE_E2E_AI_MODE: "mock",
    },
    stdio: ["ignore", "pipe", "pipe"],
  });
  child.on("error", (error) => {
    child.e2eSpawnError = error;
  });
  child.stdout.on("data", (chunk) => {
    output = appendBounded(output, chunk.toString());
  });
  child.stderr.on("data", (chunk) => {
    output = appendBounded(output, chunk.toString());
  });
  const flushLog = () => writeFile(logPath, output, "utf8");

  let handshake;
  try {
    handshake = await waitForJSON(handshakePath, child);
    assert.equal(
      handshake.pid,
      child.pid,
      "handshake PID does not match launched artifact",
    );
    await delay(STARTUP_SETTLE_MS);
    if (child.exitCode !== null || child.signalCode !== null) {
      fail(
        "launch",
        `artifact exited during settle window (code=${child.exitCode}, signal=${child.signalCode})`,
      );
    }
  } catch (error) {
    signalProcessGroup(child, "SIGKILL");
    await flushLog();
    throw error;
  }
  assert.equal(handshake.runId, runId, "handshake runId does not match launch");
  log(
    "launch",
    `runId=${runId} artifact pid=${child.pid} endpoint=${handshake.url}`,
  );
  return {
    child,
    client: new PackagedE2EClient({ url: handshake.url, token }),
    flushLog,
    stopped: false,
  };
}

function fixtureManifest(status = "not-run") {
  return CORE_FIXTURE_IDS.map((id) => ({
    id,
    driverImplemented: true,
    status,
  }));
}

export function packagedE2EFixtureResultPatch(
  manifest,
  { id, status, failure },
) {
  assert(
    status === "passed" || status === "failed",
    `unsupported fixture result status: ${status}`,
  );
  const fixtureIndex = manifest.fixtures.findIndex((entry) => entry.id === id);
  assert.notEqual(fixtureIndex, -1, `unknown packaged fixture: ${id}`);
  const expectedIndex = manifest.fixtures.findIndex(
    (entry) => entry.status !== "passed",
  );
  assert.equal(
    fixtureIndex,
    expectedIndex,
    `fixture result out of order: ${id}`,
  );
  assert.equal(
    manifest.fixtures[fixtureIndex].status,
    "not-run",
    `fixture result already recorded: ${id}`,
  );
  if (status === "failed") {
    assert(
      typeof failure === "string" && failure.length > 0,
      `failed fixture result is missing failure detail: ${id}`,
    );
  }

  return {
    fixtures: manifest.fixtures.map((entry, index) => {
      if (index !== fixtureIndex) return entry;
      return status === "failed"
        ? { ...entry, status, failure }
        : { ...entry, status };
    }),
  };
}

export function createPackagedE2EManifest({
  expectedWailsCli = null,
  runner = runnerEvidence(),
  recordedAt = new Date().toISOString(),
  runId = randomBytes(32).toString("hex"),
} = {}) {
  assertPackagedRunId(runId);
  return {
    goal: "P9-G10/G11/G12/G13/G14/G15/G16/G17/G18/G23/G24 + P12-BUG-02",
    artifact: null,
    sha256: null,
    commit: null,
    gitMetadataAvailable: false,
    workingTreeDirty: null,
    gitStatusSha256: null,
    gitStatusPorcelain: null,
    sourceFingerprintSha256: null,
    sourceFingerprintScope: null,
    sourceFingerprintFileCount: null,
    sourceFingerprintStableAfterBuild: false,
    sourceFingerprintVerifiedAt: null,
    artifactReused: false,
    artifactReuseSourceRecordedAt: null,
    runner,
    expectedWailsCli,
    wailsCli: null,
    buildTags: ["desktop", "production", "e2e"],
    screenshot: null,
    recordedAt,
    runId,
    fixtures: fixtureManifest(),
    phase: "initializing",
    status: "running",
  };
}

export function markPackagedE2EManifestFailed(
  manifest,
  error,
  completedAt = new Date().toISOString(),
) {
  return {
    ...manifest,
    status: "failed",
    failure: String(error?.message ?? error),
    completedAt,
  };
}
export async function runPackagedE2EManifestLifecycle({
  manifest,
  writeManifest,
  run,
  now = () => new Date().toISOString(),
  onFailureWriteError = () => {},
}) {
  await writeManifest(manifest);

  const checkpoint = async (patch) => {
    Object.assign(manifest, patch);
    await writeManifest(manifest);
  };

  try {
    return await run({ manifest, checkpoint });
  } catch (error) {
    Object.assign(
      manifest,
      markPackagedE2EManifestFailed(manifest, error, now()),
    );
    try {
      await writeManifest(manifest);
    } catch (writeError) {
      try {
        onFailureWriteError(writeError);
      } catch {
        // Preserve the original packaged-E2E failure.
      }
    }
    throw error;
  }
}

async function main() {
  assert(
    Number(dryRun) + Number(verifyEvidence) <= 1,
    "choose only one of --dry-run or --verify-evidence",
  );
  if (verifyEvidence) {
    const evidence = await verifyWindowsPackagedE2EEvidence();
    log(
      "verify-evidence",
      `${evidence.fixtureCount}/${CORE_FIXTURE_IDS.length} passed; sha256=${evidence.sha256}; source=${evidence.sourceFingerprintSha256}`,
    );
    return;
  }
  if (dryRun) {
    const pinned = await pinnedWailsVersion();
    assert.equal(CORE_FIXTURE_IDS.length, 24);
    assert.equal(new Set(CORE_FIXTURE_IDS).size, 24);
    log("plan", `wails3 CLI pin required for a real run: ${pinned}`);
    log("plan", "test build tags: desktop,production,e2e");
    for (const fixture of fixtureManifest("source-validated")) {
      log(
        "plan-fixture",
        `${fixture.id} - driver code present (artifact not launched)`,
      );
    }
    log(
      "dry-run",
      "source-level plan validated; real packaged execution remains U",
    );
    return;
  }

  let virtualDisplay;
  let currentLaunch;
  let fixture;
  const manifest = createPackagedE2EManifest();
  const manifestPath = path.join(evidenceDir, "manifest.json");
  log("identity", `runId=${manifest.runId}`);
  let reusableArtifactEvidence = null;
  let reusableArtifactEvidenceError = null;
  let exitCode = 0;

  try {
    await mkdir(evidenceDir, { recursive: true });
    if (skipBuild) {
      try {
        reusableArtifactEvidence = await readPackagedE2EManifest(manifestPath);
      } catch (error) {
        reusableArtifactEvidenceError = error;
      }
    }
    await runPackagedE2EManifestLifecycle({
      manifest,
      writeManifest: (value) =>
        writeFile(manifestPath, `${JSON.stringify(value, null, 2)}\n`, "utf8"),
      onFailureWriteError: (error) => {
        console.error(
          `[packaged-e2e] FAIL could not persist failure manifest: ${error?.stack ?? error}`,
        );
      },
      run: async ({ checkpoint }) => {
        if (reusableArtifactEvidenceError) {
          fail(
            "source-evidence",
            `cannot read prior packaged manifest for skip-build: ${reusableArtifactEvidenceError.message}`,
          );
        }
        const pinned = await pinnedWailsVersion();
        await checkpoint({ expectedWailsCli: pinned, phase: "toolchain" });
        const installed = installedWailsVersion();
        if (!installed)
          fail(
            "toolchain",
            `wails3 CLI not found (PATH or KOYORI_IDE_PINNED_WAILS3); install ${pinned}`,
          );
        if (installed.version !== pinned) {
          fail(
            "toolchain",
            `wails3 CLI is ${installed.version} but go.mod pins ${pinned}`,
          );
        }
        log(
          "toolchain",
          `wails3 ${installed.version} matches go.mod (${installed.command})`,
        );

        await checkpoint({
          wailsCli: installed.version,
          phase: "source-evidence",
        });
        const tree = captureWorkingTreeEvidence();
        const sourceBeforeBuild = await captureSourceFingerprint();
        if (!tree.commit)
          log(
            "evidence",
            "empty .git: binding evidence to source fingerprint instead of a commit",
          );
        else if (tree.workingTreeDirty)
          log(
            "evidence",
            `dirty working tree: HEAD ${tree.commit} plus porcelain sha256 ${tree.gitStatusSha256}; source fingerprint binds local edits`,
          );
        await checkpoint({
          commit: tree.commit,
          gitMetadataAvailable: tree.gitMetadataAvailable,
          workingTreeDirty: tree.workingTreeDirty,
          gitStatusSha256: tree.gitStatusSha256,
          gitStatusPorcelain: tree.gitStatusPorcelain,
          sourceFingerprintSha256: sourceBeforeBuild.sha256,
          sourceFingerprintScope: SOURCE_FINGERPRINT_SCOPE,
          sourceFingerprintFileCount: sourceBeforeBuild.files.length,
          phase: skipBuild ? "frontend-probes" : "frontend-build",
        });

        if (!skipBuild) {
          await buildPackagedFrontend();
          await checkpoint({ phase: "frontend-probes" });
          await verifyFrontendE2EProbeMarkers();
          await checkpoint({ phase: "wails-build" });
          log("build", "wails3 build -tags desktop,production,e2e DEV=false");
          // P19 CI 修复：alpha2.111 的 build Taskfile 要求显式 DEV 变量
          // （与 ci.yml wails-build 腿的修复一致），否则 precondition 直接失败。
          const build = spawnSync(
            installed.command,
            ["build", "-tags", "desktop,production,e2e", "DEV=false"],
            {
              cwd: root,
              stdio: "inherit",
              env: { ...process.env, VITE_KOYORI_IDE_E2E_MONACO: "1" },
            },
          );
          if (build.status !== 0)
            fail("build", `wails3 build exited ${build.status}`);
          await checkpoint({ phase: "source-verification" });
          assertSourceFingerprintUnchanged(
            sourceBeforeBuild,
            await captureSourceFingerprint(),
            "build",
          );
          await checkpoint({ sourceFingerprintStableAfterBuild: true });
        } else {
          log(
            "build",
            "KOYORI_IDE_E2E_SKIP_BUILD=1: reusing existing artifact under bin/",
          );
          await verifyFrontendE2EProbeMarkers();
        }

        await checkpoint({ phase: "artifact" });
        const artifact = await locateArtifact();
        if (!artifact) fail("build", "no packaged artifact found under bin/");
        const digest = await sha256(artifact);
        const artifactRelative = path.relative(root, artifact);
        if (skipBuild) {
          validateReusableArtifactEvidence(reusableArtifactEvidence, {
            sourceFingerprintScope: SOURCE_FINGERPRINT_SCOPE,
            sourceFingerprintSha256: sourceBeforeBuild.sha256,
            sourceFingerprintFileCount: sourceBeforeBuild.files.length,
            artifact: artifactRelative,
            sha256: digest,
            wailsCli: installed.version,
            buildTags: manifest.buildTags,
            commit: tree.commit,
            workingTreeDirty: tree.workingTreeDirty,
            gitStatusSha256: tree.gitStatusSha256,
          });
        }
        await checkpoint({
          artifact: artifactRelative,
          sha256: digest,
          sourceFingerprintStableAfterBuild: true,
          artifactReused: skipBuild,
          artifactReuseSourceRecordedAt: skipBuild
            ? (reusableArtifactEvidence.recordedAt ?? null)
            : null,
          phase: "fixture-setup",
        });
        fixture = await createFixtureWorkspace();
        await checkpoint({ phase: "fixtures" });
        log("evidence", `sha256=${digest}`);
        log("evidence", `commit=${tree.commit}`);
        log(
          "evidence",
          `workingTreeDirty=${tree.workingTreeDirty} gitStatusSha256=${tree.gitStatusSha256}`,
        );

        virtualDisplay = await startVirtualDisplay();
        currentLaunch = await launchArtifact({
          artifact,
          fixture,
          display: virtualDisplay.display,
          index: 1,
          runId: manifest.runId,
        });
        let screenshot = null;
        try {
          screenshot = await captureWindowScreenshot(
            currentLaunch.child.pid,
            path.join(evidenceDir, "window.png"),
            virtualDisplay?.display,
          );
        } catch (error) {
          log("evidence", `screenshot unavailable: ${error.message}`);
        }
        manifest.screenshot = screenshot;

        const completed = await runCoreFixtures({
          client: currentLaunch.client,
          workspace: fixture.workspace,
          filePath: fixture.filePath,
          initialContent: fixture.initialContent,
          savedContent: fixture.savedContent,
          dirtyContent: fixture.dirtyContent,
          restart: async () => {
            log(
              "fixture",
              "kill-restart-recovery: sending SIGKILL to packaged process group",
            );
            await stopArtifact(currentLaunch, "SIGKILL");
            currentLaunch = await launchArtifact({
              artifact,
              fixture,
              display: virtualDisplay.display,
              index: 2,
              runId: manifest.runId,
            });
            return currentLaunch.client;
          },
          onEvidence: (evidence) => {
            Object.assign(manifest, evidence);
          },
          onFixtureResult: async (result) => {
            await checkpoint(packagedE2EFixtureResultPatch(manifest, result));
          },
        });
        if (
          completed.length !== CORE_FIXTURE_IDS.length ||
          completed.some((id, index) => id !== CORE_FIXTURE_IDS[index])
        ) {
          fail(
            "fixtures",
            `completed ${completed.length}/${CORE_FIXTURE_IDS.length} fixtures`,
          );
        }
        await checkpoint({ phase: "source-final-verification" });
        assertSourceFingerprintUnchanged(
          sourceBeforeBuild,
          await captureSourceFingerprint(),
          "packaged fixtures",
        );
        await checkpoint({
          fixtures: fixtureManifest().map((entry) => ({
            ...entry,
            status: completed.includes(entry.id) ? "passed" : "not-run",
          })),
          phase: "complete",
          status: "passed",
          sourceFingerprintVerifiedAt: new Date().toISOString(),
          completedAt: new Date().toISOString(),
        });
        log(
          "fixtures",
          `${completed.length}/${CORE_FIXTURE_IDS.length} passed against the packaged artifact`,
        );
      },
    });
  } catch (error) {
    exitCode = 1;
    console.error(`[packaged-e2e] FAIL ${error?.stack ?? error}`);
    await captureFailureScreenshot(virtualDisplay?.display);
  } finally {
    await stopArtifact(currentLaunch);
    await virtualDisplay?.stop();
    if (fixture?.directory && exitCode === 0) {
      // Give WebView2 child processes a moment to release their lockfile before
      // removing the fixture; retry once before giving up (a lingering
      // EBWebView lockfile must not turn a passed run into a failed exit).
      for (let attempt = 0; attempt < 2; attempt++) {
        try {
          await rm(fixture.directory, { recursive: true, force: true });
          break;
        } catch (error) {
          log(
            "cleanup",
            `fixture removal attempt ${attempt + 1} failed: ${error?.message ?? error}`,
          );
          await delay(1500);
        }
      }
    }
  }

  process.exit(exitCode);
}

const invokedPath = process.argv[1] ? path.resolve(process.argv[1]) : null;
if (invokedPath === fileURLToPath(import.meta.url)) {
  await main();
}
