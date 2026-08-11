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
  mkdtemp,
  readFile,
  readdir,
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

async function pinnedWailsVersion() {
  const goMod = await readFile(path.join(root, "go.mod"), "utf8");
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
  const version = `${probe.stdout}${probe.stderr}`.match(/v3\.\S+/)?.[0] ?? null;
  return version ? { command, version } : null;
}

function currentCommit() {
  const probe = spawnSync("git", ["rev-parse", "HEAD"], {
    cwd: root,
    encoding: "utf8",
  });
  if (probe.error || probe.status !== 0) return null;
  return probe.stdout.trim();
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
]);

export async function verifyFrontendE2EProbeMarkers(distPath = path.join(root, "frontend", "dist")) {
  const assetsDir = path.join(distPath, "assets");
  let assetNames;
  try {
    assetNames = await readdir(assetsDir);
  } catch (error) {
    fail("frontend-probes", `cannot read frontend dist assets: ${error.message}`);
  }
  const javascript = (await Promise.all(
    assetNames
      .filter((name) => name.endsWith(".js"))
      .map((name) => readFile(path.join(assetsDir, name), "utf8")),
  )).join("\n");
  const missing = FRONTEND_E2E_PROBE_MARKERS.filter((marker) => !javascript.includes(marker));
  if (missing.length > 0) {
    fail("frontend-probes", `E2E renderer marker(s) missing from dist: ${missing.join(", ")}`);
  }
  log("frontend-probes", `verified ${FRONTEND_E2E_PROBE_MARKERS.length} E2E renderer markers in dist`);
  return FRONTEND_E2E_PROBE_MARKERS;
}

async function buildPackagedFrontend() {
  const windows = process.platform === "win32";
  const command = windows ? "cmd.exe" : "npm";
  const args = windows ? ["/d", "/s", "/c", "npm.cmd run build"] : ["run", "build"];
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

// P9-G10: when .git is unavailable (empty directory), bind the evidence to a
// source fingerprint instead of a commit, matching the G05/G06 convention.
async function sourceFingerprint() {
  const files = [
    "main.go",
    "bootstrap_services.go",
    "services/workspace_context.go",
    "services/project_service.go",
    "services/file_service.go",
    "services/recovery_service.go",
    "services/terminal_service.go",
    "services/language_pack_runtime.go",
    "services/language_pack_service.go",
    "services/language_pack_e2e.go",
    "services/languagepacks/go.language-pack.json",
    "services/languagepacks/typescript.language-pack.json",
    "services/debug_service.go",
    "services/debug_launch.go",
    "services/debug_dap_io.go",
    "services/lsp_service.go",
    "services/lsp_service_server.go",
    "services/lsp_service_sync.go",
    "services/lsp_service_protocol.go",
    "services/toolchain_service.go",
    "services/git_service.go",
    "services/project_service.go",
    "services/git_rebase_service.go",
    "services/update_service.go",
    "services/diff_service.go",
    "services/workspace_edit_transaction.go",
    "services/diff_receipt.go",
    "services/errors.go",
    "internal/e2e/server.go",
    "internal/e2e/language_pack_g23.go",
    "internal/e2e/language_pack_builtins_g23.go",
    "frontend/src/main.ts",
    "frontend/src/lib/language.ts",
    "frontend/src/lib/languagePackRuntime.ts",
    "frontend/src/e2e/monacoProbe.ts",
    "frontend/package.json",
    "frontend/package-lock.json",
    "frontend/src/stores/toolchain.ts",
    "frontend/src/stores/terminal.ts",
    "frontend/src/components/layout/TerminalPanel.vue",
    "frontend/src/api/git.ts",
    "frontend/src/stores/git.ts",
    "frontend/src/components/layout/GitPanel.vue",
    "scripts/packaged-e2e.mjs",
    "scripts/packaged-e2e-driver.mjs",
  ].sort();
  const hash = createHash("sha256");
  for (const relative of files) {
    const digest = await sha256(path.join(root, relative));
    hash.update(relative);
    hash.update("\0");
    hash.update(digest);
    hash.update("\n");
  }
  return hash.digest("hex");
}

async function locateArtifact() {
  // P9-G10: prefer the canonical artifact name. Size-sorted selection can
  // pick up stale test binaries that lack the e2e build tag.
  const canonical = process.platform === "win32" ? "koyori-ide.exe" : "koyori-ide";
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
      fail("launch", `artifact exited before handshake (code=${child.exitCode}, signal=${child.signalCode})`);
    }
    try {
      return JSON.parse(await readFile(filePath, "utf8"));
    } catch {
      await delay(100);
    }
  }
  fail("launch", `timed out waiting for handshake ${path.relative(root, filePath)}`);
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
  const child = spawn("Xvfb", [
    display,
    "-screen", "0", "1280x1024x24",
    "-nolisten", "tcp",
  ], {
    cwd: root,
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
  await delay(750);
  if (child.e2eSpawnError) {
    await writeFile(logPath, output, "utf8");
    fail("xvfb", `Xvfb spawn failed: ${child.e2eSpawnError.message}`);
  }
  if (child.exitCode !== null || child.signalCode !== null) {
    await writeFile(logPath, output, "utf8");
    fail("xvfb", `Xvfb exited during startup (code=${child.exitCode}, signal=${child.signalCode})`);
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
      if (child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
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
    const encoded = Buffer.from("Add-Type -AssemblyName System.Drawing\nAdd-Type @\"\nusing System; using System.Runtime.InteropServices; using System.Text;\npublic struct G10Rect { public int Left; public int Top; public int Right; public int Bottom; }\npublic static class G10Cap { public delegate bool EnumWindowsProc(IntPtr h, IntPtr l);\n[DllImport(\"user32.dll\")] public static extern bool EnumWindows(EnumWindowsProc c, IntPtr l);\n[DllImport(\"user32.dll\")] public static extern bool IsWindowVisible(IntPtr h);\n[DllImport(\"user32.dll\")] public static extern uint GetWindowThreadProcessId(IntPtr h, out uint p);\n[DllImport(\"user32.dll\", CharSet=CharSet.Unicode)] public static extern int GetWindowText(IntPtr h, StringBuilder s, int m);\n[DllImport(\"user32.dll\")] public static extern bool GetWindowRect(IntPtr h, out G10Rect r);\npublic static IntPtr Find(int pid) { IntPtr found=IntPtr.Zero; EnumWindows((h,l)=>{ uint owner; GetWindowThreadProcessId(h,out owner); if(owner!=(uint)pid||!IsWindowVisible(h))return true; var s=new StringBuilder(256);GetWindowText(h,s,s.Capacity);if(s.ToString().IndexOf(\"koyori-ide\",StringComparison.OrdinalIgnoreCase)<0)return true; found=h; return false;},IntPtr.Zero); return found; } }\n\"@\n$processId = [int]$env:G10_PID; $output = $env:G10_OUT\n$deadline=[DateTime]::UtcNow.AddSeconds(20); $handle=[IntPtr]::Zero\nwhile([DateTime]::UtcNow -lt $deadline){$handle=[G10Cap]::Find($processId);if($handle -ne [IntPtr]::Zero){break};Start-Sleep -Milliseconds 100}\nif($handle -eq [IntPtr]::Zero){throw \"no visible koyori-ide window\"}\n$rect=New-Object G10Rect\nif(-not [G10Cap]::GetWindowRect($handle,[ref]$rect)){throw \"GetWindowRect failed\"}\n$w=$rect.Right-$rect.Left;$h=$rect.Bottom-$rect.Top\nif($w -lt 200 -or $h -lt 150){throw \"unexpected dimensions ${w}x${h}\"}\n$bitmap=New-Object System.Drawing.Bitmap($w,$h)\n$graphics=[System.Drawing.Graphics]::FromImage($bitmap)\ntry{$graphics.CopyFromScreen($rect.Left,$rect.Top,0,0,$bitmap.Size);$bitmap.Save($output,[System.Drawing.Imaging.ImageFormat]::Png);($w.ToString() + 'x' + $h.ToString())}finally{$graphics.Dispose();$bitmap.Dispose()}", "utf16le").toString("base64");
    const result = spawnSync("powershell.exe", ["-NoProfile", "-NonInteractive", "-EncodedCommand", encoded], {
      encoding: "utf8",
      windowsHide: true,
      env: { ...process.env, G10_PID: String(pid), G10_OUT: outputPath },
    });
    if (result.status !== 0) {
      await writeFile(path.join(evidenceDir, "screenshot-error.txt"), `${result.stderr || result.stdout}\n`, "utf8");
      return null;
    }
    const info = await stat(outputPath);
    const dims = result.stdout.trim().split(/\r?\n/).at(-1).split("x").map(Number);
    const meta = { width: dims[0], height: dims[1] };
    return { file: path.basename(outputPath), bytes: info.size, ...meta };
  }
  if (display && process.platform === "linux") {
    const capture = spawnSync("import", ["-window", "root", outputPath], {
      encoding: "utf8",
      env: { ...process.env, DISPLAY: display },
    });
    if (capture.error || capture.status !== 0) throw new Error(`X11 capture failed: ${capture.stderr || capture.stdout}`);
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
  const directory = await mkdtemp(path.join(os.tmpdir(), "koyori-ide-packaged-e2e-"));
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
  await writeFile(path.join(workspace, "go.mod"), "module fixture\n\ngo 1.25.0\n", "utf8");
  // Keep the fixture out of the Go toolchain's ./... scan by placing
  // it in its own module subdirectory under the evidence folder.
  const fixtureDir = path.join(evidenceDir, "fixtures");
  await mkdir(fixtureDir, { recursive: true });
  await writeFile(path.join(fixtureDir, "go.mod"), "module koyori-e2e-fixture\n\ngo 1.25.0\n", "utf8");
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

async function launchArtifact({ artifact, fixture, display, index }) {
  const token = randomBytes(32).toString("hex");
  const handshakePath = path.join(evidenceDir, `handshake-${index}.json`);
  const logPath = path.join(evidenceDir, `launch-${index}.log`);
  await rm(handshakePath, { force: true });

  let output = "";
  await mkdir(path.join(fixture.configDir, `launch-${index}`), { recursive: true });
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
      LOCALAPPDATA: path.join(fixture.configDir, "appdata", "Local"),
      KOYORI_IDE_E2E: "1",
      KOYORI_IDE_E2E_TOKEN: token,
      KOYORI_IDE_E2E_HANDSHAKE: handshakePath,
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
    assert.equal(handshake.pid, child.pid, "handshake PID does not match launched artifact");
    await delay(STARTUP_SETTLE_MS);
    if (child.exitCode !== null || child.signalCode !== null) {
      fail("launch", `artifact exited during settle window (code=${child.exitCode}, signal=${child.signalCode})`);
    }
  } catch (error) {
    signalProcessGroup(child, "SIGKILL");
    await flushLog();
    throw error;
  }

  log("launch", `artifact pid=${child.pid} endpoint=${handshake.url}`);
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

async function main() {
const pinned = await pinnedWailsVersion();

if (dryRun) {
  assert.equal(CORE_FIXTURE_IDS.length, 24);
  assert.equal(new Set(CORE_FIXTURE_IDS).size, 24);
  log("plan", `wails3 CLI pin required for a real run: ${pinned}`);
  log("plan", "test build tags: desktop,production,e2e");
  for (const fixture of fixtureManifest("source-validated")) {
    log("plan-fixture", `${fixture.id} — driver code present (artifact not launched)`);
  }
  log("dry-run", "source-level plan validated; real packaged execution remains U");
  process.exit(0);
}

let virtualDisplay;
let currentLaunch;
let fixture;
let manifest;
let manifestPath;
let exitCode = 0;

try {
  await mkdir(evidenceDir, { recursive: true });
  const installed = installedWailsVersion();
  if (!installed) fail("toolchain", `wails3 CLI not found (PATH or KOYORI_IDE_PINNED_WAILS3); install ${pinned}`);
  if (installed.version !== pinned) {
    fail("toolchain", `wails3 CLI is ${installed.version} but go.mod pins ${pinned}`);
  }
  log("toolchain", `wails3 ${installed.version} matches go.mod (${installed.command})`);

  const commit = currentCommit();
  const fingerprint = await sourceFingerprint();
  if (!commit) log("evidence", "empty .git: binding evidence to source fingerprint instead of a commit");

  if (!skipBuild) {
    await buildPackagedFrontend();
    await verifyFrontendE2EProbeMarkers();
    log("build", "wails3 build -tags desktop,production,e2e");
    const build = spawnSync(installed.command, ["build", "-tags", "desktop,production,e2e"], {
      cwd: root,
      stdio: "inherit",
      env: { ...process.env, VITE_KOYORI_IDE_E2E_MONACO: "1" },
    });
    if (build.status !== 0) fail("build", `wails3 build exited ${build.status}`);
  } else {
    log("build", "KOYORI_IDE_E2E_SKIP_BUILD=1: reusing existing artifact under bin/");
    await verifyFrontendE2EProbeMarkers();
  }

  const artifact = await locateArtifact();
  if (!artifact) fail("build", "no packaged artifact found under bin/");
  const digest = await sha256(artifact);
  fixture = await createFixtureWorkspace();
  manifest = {
    goal: "P9-G10/G11/G12/G13/G14/G15/G16/G17/G18/G23/G24",
    artifact: path.relative(root, artifact),
    sha256: digest,
    commit,
    gitMetadataAvailable: commit !== null,
    sourceFingerprintSha256: fingerprint,
    runner: runnerEvidence(),
    wailsCli: installed.version,
    buildTags: ["desktop", "production", "e2e"],
    screenshot: null,
    recordedAt: new Date().toISOString(),
    fixtures: fixtureManifest(),
    status: "running",
  };
  manifestPath = path.join(evidenceDir, "manifest.json");
  await writeFile(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`, "utf8");
  log("evidence", `sha256=${digest}`);
  log("evidence", `commit=${commit}`);


  virtualDisplay = await startVirtualDisplay();
  currentLaunch = await launchArtifact({
    artifact,
    fixture,
    display: virtualDisplay.display,
    index: 1,
  });
  let screenshot = null;
  try {
    screenshot = await captureWindowScreenshot(currentLaunch.child.pid, path.join(evidenceDir, "window.png"), virtualDisplay?.display);
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
      log("fixture", "kill-restart-recovery: sending SIGKILL to packaged process group");
      await stopArtifact(currentLaunch, "SIGKILL");
      currentLaunch = await launchArtifact({
        artifact,
        fixture,
        display: virtualDisplay.display,
        index: 2,
      });
      return currentLaunch.client;
    },
    onEvidence: (evidence) => {
      Object.assign(manifest, evidence);
    },
  });
  manifest.fixtures = fixtureManifest().map((entry) => ({
    ...entry,
    status: completed.includes(entry.id) ? "passed" : "not-run",
  }));
  manifest.status = "passed";
  manifest.completedAt = new Date().toISOString();
  await writeFile(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`, "utf8");
  log("fixtures", `${completed.length}/${CORE_FIXTURE_IDS.length} passed against the packaged artifact`);
} catch (error) {
  exitCode = 1;
  console.error(`[packaged-e2e] FAIL ${error?.stack ?? error}`);
  if (manifest && manifestPath) {
    manifest.status = "failed";
    manifest.failure = String(error?.message ?? error);
    manifest.completedAt = new Date().toISOString();
    await writeFile(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`, "utf8");
  }
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
        log("cleanup", `fixture removal attempt ${attempt + 1} failed: ${error?.message ?? error}`);
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
