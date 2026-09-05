#!/usr/bin/env node

import assert from "node:assert/strict";
import { createHash, randomBytes } from "node:crypto";
import { spawn, spawnSync } from "node:child_process";
import {
  mkdir,
  mkdtemp,
  readFile,
  rm,
  stat,
  writeFile,
} from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { PackagedE2EClient } from "./packaged-e2e-driver.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const evidenceDir = path.join(root, "build", "e2e-evidence", "p9-g05");
const pinnedCLIRelative = path.join(
  "build",
  "e2e-evidence",
  "p9-g05",
  "wails-cli",
  process.platform === "win32" ? "wails3.exe" : "wails3",
);
const startupTimeout = 600_000;
const processStopTimeout = 8_000;
const dryRun = process.argv.includes("--dry-run");

function log(stage, detail) {
  console.log(`[g05-packaged-e2e] ${stage}: ${detail}`);
}

function sha256Bytes(value) {
  return createHash("sha256").update(value).digest("hex");
}

async function sha256File(filePath) {
  return sha256Bytes(await readFile(filePath));
}

async function pinnedVersion() {
  const goMod = await readFile(path.join(root, "go.mod"), "utf8");
  const match = goMod.match(/^\s*github\.com\/wailsapp\/wails\/v3\s+(v\S+)/m);
  assert(match, "Wails v3 is not pinned in go.mod");
  return match[1];
}

function hostEnvironment(overrides = {}) {
  const environment = { ...process.env, ...overrides };
  for (const name of ["GOOS", "GOARCH", "GOFLAGS", "CGO_ENABLED"])
    delete environment[name];
  return environment;
}

function spawnCaptured(command, args, options = {}) {
  let output = "";
  const child = spawn(command, args, {
    cwd: root,
    stdio: ["ignore", "pipe", "pipe"],
    windowsHide: false,
    ...options,
  });
  child.on("error", (error) => {
    child.spawnError = error;
  });
  child.stdout.on("data", (chunk) => {
    output += chunk.toString();
  });
  child.stderr.on("data", (chunk) => {
    output += chunk.toString();
  });
  return { child, output: () => output };
}

async function runCommand(command, args, environment, logPath, cwd = root) {
  const launch = spawnCaptured(command, args, { cwd, env: environment });
  const result = await new Promise((resolve, reject) => {
    launch.child.once("error", reject);
    launch.child.once("exit", (code, signal) => resolve({ code, signal }));
  });
  const output = launch.output();
  await writeFile(logPath, output, "utf8");
  if (result.code !== 0) {
    throw new Error(
      `${command} ${args.join(" ")} failed (code=${result.code}, signal=${result.signal})`,
    );
  }
  return { ...result, output };
}

async function verifiedCLI(workDirectory, pin) {
  const supplied = process.env.KOYORI_IDE_G05_E2E_WAILS3?.trim();
  const candidate = supplied
    ? path.resolve(supplied)
    : path.resolve(root, pinnedCLIRelative);
  try {
    const info = await stat(candidate);
    assert(info.isFile(), `Wails CLI is not a file: ${candidate}`);
    const version = spawnSync(candidate, ["version"], {
      encoding: "utf8",
      windowsHide: true,
    });
    const reported = `${version.stdout}${version.stderr}`.trim();
    assert.equal(
      version.status,
      0,
      `Wails CLI version command failed: ${reported}`,
    );
    assert.equal(reported, pin, `Wails CLI is ${reported}, want ${pin}`);
    return {
      executable: candidate,
      directory: path.dirname(candidate),
      version: pin,
      source: supplied
        ? "provided-verified-path"
        : "existing-pinned-evidence-cli",
      sha256: await sha256File(candidate),
      installLog: null,
    };
  } catch (error) {
    if (supplied) throw error;
  }

  const directory = path.join(workDirectory, "wails-cli");
  await mkdir(directory, { recursive: true });
  const executable = path.join(
    directory,
    process.platform === "win32" ? "wails3.exe" : "wails3",
  );
  const logPath = path.join(evidenceDir, "g05-wails-cli-install.log");
  await runCommand(
    "go",
    ["install", `github.com/wailsapp/wails/v3/cmd/wails3@${pin}`],
    hostEnvironment({ GOBIN: directory }),
    logPath,
  );
  const version = spawnSync(executable, ["version"], {
    encoding: "utf8",
    windowsHide: true,
  });
  const reported = `${version.stdout}${version.stderr}`.trim();
  assert.equal(
    reported,
    pin,
    `temporary Wails CLI is ${reported}, want ${pin}`,
  );
  return {
    executable,
    directory,
    version: pin,
    source: "go-install",
    sha256: await sha256File(executable),
    installLog: path.basename(logPath),
  };
}

async function wait(milliseconds) {
  await new Promise((resolve) => setTimeout(resolve, milliseconds));
}

async function waitForHandshake(filePath, child) {
  const deadline = Date.now() + startupTimeout;
  while (Date.now() < deadline) {
    if (child.spawnError)
      throw new Error(`artifact spawn failed: ${child.spawnError.message}`);
    if (child.exitCode !== null || child.signalCode !== null) {
      throw new Error(
        `artifact exited before handshake (code=${child.exitCode}, signal=${child.signalCode})`,
      );
    }
    try {
      return JSON.parse(await readFile(filePath, "utf8"));
    } catch (error) {
      if (error?.code !== "ENOENT" && !(error instanceof SyntaxError))
        throw error;
    }
    await wait(100);
  }
  throw new Error(`timed out waiting for handshake ${filePath}`);
}

async function stopProcess(launch) {
  if (!launch?.child || launch.child.exitCode !== null) return;
  if (process.platform === "win32") {
    spawnSync("taskkill", ["/PID", String(launch.child.pid), "/T", "/F"], {
      encoding: "utf8",
      windowsHide: true,
    });
  } else {
    try {
      process.kill(-launch.child.pid, "SIGTERM");
    } catch (error) {
      if (error?.code !== "ESRCH") throw error;
    }
  }
  await Promise.race([
    new Promise((resolve) => launch.child.once("exit", resolve)),
    wait(processStopTimeout),
  ]);
  if (launch.child.exitCode === null && process.platform !== "win32") {
    try {
      process.kill(-launch.child.pid, "SIGKILL");
    } catch (error) {
      if (error?.code !== "ESRCH") throw error;
    }
  }
  await launch.flushLog();
}

async function launchArtifact(artifact, configDirectory, workDirectory, index) {
  const token = randomBytes(32).toString("hex");
  const runId = randomBytes(32).toString("hex");
  const launchDirectory = path.join(workDirectory, `launch-${index}`);
  const handshakePath = path.join(launchDirectory, "handshake.json");
  const logPath = path.join(evidenceDir, `g05-packaged-launch-${index}.log`);
  await mkdir(launchDirectory, { recursive: true });
  await rm(handshakePath, { force: true });
  const launch = spawnCaptured(artifact, [], {
    env: hostEnvironment({
      KOYORI_IDE_E2E: "1",
      KOYORI_IDE_E2E_TOKEN: token,
      KOYORI_IDE_E2E_HANDSHAKE: handshakePath,
      KOYORI_IDE_E2E_RUN_ID: runId,
      XDG_CONFIG_HOME: configDirectory,
      APPDATA: configDirectory,
      LOCALAPPDATA: configDirectory,
      XDG_CACHE_HOME: path.join(configDirectory, "cache"),
    }),
  });
  launch.flushLog = () => writeFile(logPath, launch.output(), "utf8");
  try {
    const handshake = await waitForHandshake(handshakePath, launch.child);
    assert.equal(
      handshake.pid,
      launch.child.pid,
      "handshake PID does not match launched process",
    );
    await wait(1_500);
    assert.equal(
      launch.child.exitCode,
      null,
      "artifact exited during startup settle",
    );
    log("launch", `pid=${launch.child.pid} endpoint=${handshake.url}`);
    return {
      ...launch,
      handshake,
      client: new PackagedE2EClient({ url: handshake.url, token }),
    };
  } catch (error) {
    await stopProcess({ ...launch, flushLog: launch.flushLog });
    throw error;
  }
}

async function productionHookAbsence() {
  const logPath = path.join(evidenceDir, "g05-production-frontend-build.log");
  const environment = hostEnvironment();
  delete environment.VITE_KOYORI_IDE_E2E_WORKSPACE;
  const command = process.platform === "win32" ? "cmd.exe" : "npm";
  const args =
    process.platform === "win32"
      ? ["/d", "/s", "/c", "npm.cmd run build"]
      : ["run", "build"];
  await runCommand(
    command,
    args,
    environment,
    logPath,
    path.join(root, "frontend"),
  );
  const dist = path.join(root, "frontend", "dist");
  const files = [];
  const scan = async (directory) => {
    let entries;
    try {
      entries = await (
        await import("node:fs/promises")
      ).readdir(directory, { withFileTypes: true });
    } catch {
      return;
    }
    for (const entry of entries) {
      const full = path.join(directory, entry.name);
      if (entry.isDirectory()) await scan(full);
      else if (entry.isFile() && /\.(js|html)$/.test(entry.name))
        files.push(full);
    }
  };
  await scan(dist);
  const forbiddenMarkers = [
    "__koyoriIdeRunG05WorkspaceProbe",
    "e2e:g05-workspace-result",
  ];
  const violations = [];
  for (const file of files) {
    const content = await readFile(file, "utf8");
    for (const marker of forbiddenMarkers)
      if (content.includes(marker)) violations.push(path.relative(root, file));
  }
  await rm(dist, { recursive: true, force: true });
  assert.deepEqual(
    violations,
    [],
    `production frontend contains G05 probe markers: ${violations.join(", ")}`,
  );
  return {
    checkedFiles: files.length,
    forbiddenMarkers,
    buildLog: path.basename(logPath),
    buildLogSha256: await sha256File(logPath),
  };
}

async function captureWindow(pid, role, outputPath) {
  if (process.platform !== "win32")
    throw new Error("G05 packaged window capture currently requires Windows");
  const script = String.raw`
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new()
Add-Type -AssemblyName System.Drawing
Add-Type @"
using System;
using System.Runtime.InteropServices;
using System.Text;
public struct GugaRect { public int Left; public int Top; public int Right; public int Bottom; }
public static class GugaWindowCapture {
  public delegate bool EnumWindowsProc(IntPtr handle, IntPtr lParam);
  [DllImport("user32.dll")] public static extern bool EnumWindows(EnumWindowsProc callback, IntPtr lParam);
  [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr handle);
  [DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr handle, out uint pid);
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern int GetWindowText(IntPtr handle, StringBuilder text, int max);
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr handle, out GugaRect rect);
  public static IntPtr Find(int processId, string role) {
    IntPtr found = IntPtr.Zero;
    EnumWindows(delegate(IntPtr handle, IntPtr _) {
      uint owner; GetWindowThreadProcessId(handle, out owner);
      if (owner != (uint)processId || !IsWindowVisible(handle)) return true;
      var text = new StringBuilder(256); GetWindowText(handle, text, text.Capacity);
      string title = text.ToString();
      if (role == "ai" && title.IndexOf("koyori-ide AI", StringComparison.OrdinalIgnoreCase) < 0) return true;
      if (role == "main" && title.IndexOf("koyori-ide AI", StringComparison.OrdinalIgnoreCase) >= 0) return true;
      found = handle; return false;
    }, IntPtr.Zero);
    return found;
  }
}
"@
$processId = [int]$env:KOYORI_IDE_CAPTURE_PID
$role = $env:KOYORI_IDE_CAPTURE_ROLE
$output = $env:KOYORI_IDE_CAPTURE_PATH
$deadline = [DateTime]::UtcNow.AddSeconds(30)
$handle = [IntPtr]::Zero
while ([DateTime]::UtcNow -lt $deadline) {
  $handle = [GugaWindowCapture]::Find($processId, $role)
  if ($handle -ne [IntPtr]::Zero) { break }
  Start-Sleep -Milliseconds 100
}
if ($handle -eq [IntPtr]::Zero) { throw "no visible $role window for process $processId" }
$rect = New-Object GugaRect
if (-not [GugaWindowCapture]::GetWindowRect($handle, [ref]$rect)) { throw "GetWindowRect failed" }
$width = $rect.Right - $rect.Left; $height = $rect.Bottom - $rect.Top
if ($width -lt 400 -or $height -lt 300) { throw "unexpected $role dimensions $($width)x$($height)" }
$bitmap = New-Object System.Drawing.Bitmap($width, $height)
$graphics = [System.Drawing.Graphics]::FromImage($bitmap)
try {
  $graphics.CopyFromScreen($rect.Left, $rect.Top, 0, 0, $bitmap.Size)
  $bitmap.Save($output, [System.Drawing.Imaging.ImageFormat]::Png)
  $colours = New-Object 'System.Collections.Generic.HashSet[int]'
  for ($x = 0; $x -lt $width; $x += [Math]::Max(1, [int]($width / 32))) {
    for ($y = 0; $y -lt $height; $y += [Math]::Max(1, [int]($height / 24))) { [void]$colours.Add($bitmap.GetPixel($x, $y).ToArgb()) }
  }
  [pscustomobject]@{ width=$width; height=$height; sampledUniqueColours=$colours.Count } | ConvertTo-Json -Compress
} finally { $graphics.Dispose(); $bitmap.Dispose() }
`;
  const result = spawnSync(
    "powershell.exe",
    ["-NoProfile", "-NonInteractive", "-Command", script],
    {
      encoding: "utf8",
      windowsHide: true,
      env: hostEnvironment({
        KOYORI_IDE_CAPTURE_PID: String(pid),
        KOYORI_IDE_CAPTURE_ROLE: role,
        KOYORI_IDE_CAPTURE_PATH: outputPath,
      }),
    },
  );
  if (result.status !== 0)
    throw new Error(
      `capture ${role} failed: ${result.stderr || result.stdout}`,
    );
  const metadata = JSON.parse(result.stdout.trim().split(/\r?\n/).at(-1));
  const info = await stat(outputPath);
  assert(info.size > 10_000, `${role} screenshot is unexpectedly small`);
  assert(
    metadata.sampledUniqueColours > 20,
    `${role} screenshot appears blank`,
  );
  return {
    file: path.basename(outputPath),
    sha256: await sha256File(outputPath),
    bytes: info.size,
    captureMethod: "EnumWindows/GetWindowRect/CopyFromScreen",
    ...metadata,
  };
}

async function sourceFingerprint() {
  const relativeFiles = [
    "main.go",
    "services/project_service.go",
    "services/project_workspace_authority.go",
    "services/workspace_context.go",
    "services/search_service.go",
    "services/ai_service.go",
    "services/terminal_service.go",
    "services/window_service.go",
    "services/window_e2e.go",
    "internal/e2e/server.go",
    "internal/e2e/types.go",
    "frontend/src/main.ts",
    "frontend/src/e2e/workspaceProbe.ts",
    "scripts/g05-packaged-e2e.mjs",
  ];
  const hash = createHash("sha256");
  const files = [];
  for (const relativePath of relativeFiles.sort()) {
    const content = await readFile(path.join(root, relativePath));
    const digest = sha256Bytes(content);
    files.push({ path: relativePath, sha256: digest, bytes: content.length });
    hash.update(relativePath);
    hash.update("\0");
    hash.update(digest);
    hash.update("\n");
  }
  return { sha256: hash.digest("hex"), files };
}

function gitCommit() {
  const result = spawnSync("git", ["rev-parse", "HEAD"], {
    cwd: root,
    encoding: "utf8",
    windowsHide: true,
  });
  return result.status === 0 ? result.stdout.trim() : null;
}

if (dryRun) {
  assert.equal(await pinnedVersion(), "v3.0.0-alpha2.111");
  log(
    "dry-run",
    "source plan validated; no packaged process was launched and evidence remains U",
  );
  process.exit(0);
}

await mkdir(evidenceDir, { recursive: true });
const evidencePath = path.join(evidenceDir, "g05-packaged-runtime.json");
const failurePath = path.join(evidenceDir, "g05-packaged-runtime.failure.json");
await Promise.all([
  rm(evidencePath, { force: true }),
  rm(failurePath, { force: true }),
]);
const workDirectory = await mkdtemp(
  path.join(os.tmpdir(), "koyori-ide-g05-e2e-"),
);
let launch;
let succeeded = false;
const startedAt = new Date().toISOString();

try {
  const configDirectory = path.join(workDirectory, "config");
  const workspaceA = path.join(workDirectory, "workspace-a");
  const workspaceB = path.join(workDirectory, "workspace-b");
  const marker = "KOYORI_IDE_G05_SHARED_WORKSPACE_OK";
  const presetName = "g05-workspace";
  await mkdir(path.join(workspaceA, ".koyori-ide", "presets"), {
    recursive: true,
  });
  await mkdir(path.join(workspaceB, ".koyori-ide", "presets"), {
    recursive: true,
  });
  await mkdir(configDirectory, { recursive: true });
  for (const workspace of [workspaceA, workspaceB]) {
    await writeFile(
      path.join(workspace, "workspace-marker.txt"),
      `${marker}\n`,
      "utf8",
    );
    await writeFile(
      path.join(workspace, ".koyori-ide", "presets", `${presetName}.json`),
      JSON.stringify(
        {
          name: presetName,
          label: "G05 workspace probe",
          description: "packaged workspace context probe",
          prompt: `Use ${marker} from this workspace`,
        },
        null,
        2,
      ),
      "utf8",
    );
  }

  const pin = await pinnedVersion();
  const cli = await verifiedCLI(workDirectory, pin);
  const production = await productionHookAbsence();
  const source = await sourceFingerprint();
  const commit = gitCommit();
  const artifact = path.join(
    root,
    "bin",
    process.platform === "win32" ? "koyori-ide.exe" : "koyori-ide",
  );
  const buildLog = path.join(evidenceDir, "g05-packaged-build.log");
  const buildEnvironment = hostEnvironment({
    PATH: `${cli.directory}${path.delimiter}${process.env.PATH ?? ""}`,
    VITE_KOYORI_IDE_E2E_WORKSPACE: "1",
  });
  await runCommand(
    cli.executable,
    ["build", "-tags", "e2e"],
    buildEnvironment,
    buildLog,
  );
  const artifactInfo = await stat(artifact);
  assert(
    artifactInfo.size > 1024 * 1024,
    "packaged artifact is unexpectedly small",
  );
  const artifactSha256 = await sha256File(artifact);

  launch = await launchArtifact(artifact, configDirectory, workDirectory, 1);
  const probe = await launch.client.command("g05-workspace-probe", {
    workspace: workspaceA,
    secondaryWorkspace: workspaceB,
    marker,
    presetName,
  });
  assert.equal(
    probe.aiWindowOpen,
    true,
    "AI window was not open after the workspace switch",
  );
  assert.equal(
    probe.aiWindowVisible,
    true,
    "AI window was not visible after the workspace switch",
  );
  assert.equal(
    probe.mainRenderer.ok,
    true,
    probe.mainRenderer.error ?? "main renderer probe failed",
  );
  assert.equal(
    probe.aiRenderer.ok,
    true,
    probe.aiRenderer.error ?? "AI renderer probe failed",
  );
  assert.equal(probe.mainRenderer.role, "main");
  assert.equal(probe.aiRenderer.role, "ai");
  assert.equal(
    probe.mainRenderer.snapshot.root.toLowerCase(),
    workspaceB.toLowerCase(),
  );
  assert.equal(
    probe.aiRenderer.snapshot.root.toLowerCase(),
    workspaceB.toLowerCase(),
  );

  const screenshots = [
    await captureWindow(
      launch.child.pid,
      "main",
      path.join(evidenceDir, "g05-packaged-main.png"),
    ),
    await captureWindow(
      launch.child.pid,
      "ai",
      path.join(evidenceDir, "g05-packaged-ai.png"),
    ),
  ];
  await launch.flushLog();
  const evidence = {
    schemaVersion: 1,
    goal: "P9-G05",
    status: "passed",
    evidenceLevel: "P",
    startedAt,
    completedAt: new Date().toISOString(),
    platform: `${process.platform}/${process.arch}`,
    osRelease: os.release(),
    nodeVersion: process.version,
    wailsVersion: cli.version,
    wailsCLI: {
      source: cli.source,
      sha256: cli.sha256,
      installLog: cli.installLog,
    },
    gitCommit: commit,
    gitMetadataAvailable: commit !== null,
    sourceFingerprintSha256: source.sha256,
    sourceFingerprintFiles: source.files,
    artifact: path.relative(root, artifact).replaceAll("\\", "/"),
    artifactSha256,
    artifactBytes: artifactInfo.size,
    buildTags: ["desktop", "production", "e2e"],
    buildLog: {
      file: path.basename(buildLog),
      sha256: await sha256File(buildLog),
    },
    productionHookAbsence: production,
    fixture: {
      primaryWorkspace: workspaceA,
      switchedWorkspace: workspaceB,
      marker,
      presetName,
    },
    workspaceSwitch: {
      primarySnapshot: probe.primarySnapshot,
      secondarySnapshot: probe.secondarySnapshot,
      aiWindowOpen: probe.aiWindowOpen,
      aiWindowVisible: probe.aiWindowVisible,
      mainRenderer: probe.mainRenderer,
      aiRenderer: probe.aiRenderer,
    },
    screenshots,
    applicationLog: {
      file: "g05-packaged-launch-1.log",
      sha256: await sha256File(
        path.join(evidenceDir, "g05-packaged-launch-1.log"),
      ),
    },
    limitations:
      commit === null
        ? [
            "The workspace has an empty .git directory; this evidence records source and artifact fingerprints and does not claim a commit.",
          ]
        : [],
  };
  await writeFile(
    evidencePath,
    `${JSON.stringify(evidence, null, 2)}\n`,
    "utf8",
  );
  succeeded = true;
  log(
    "pass",
    `artifact=${artifactSha256} evidence=${path.relative(root, evidencePath)}`,
  );
} catch (error) {
  const detail =
    error instanceof Error ? (error.stack ?? error.message) : String(error);
  await writeFile(
    failurePath,
    `${JSON.stringify(
      {
        schemaVersion: 1,
        goal: "P9-G05",
        status: "failed",
        evidenceLevel: "U",
        startedAt,
        failedAt: new Date().toISOString(),
        error: detail,
        retainedWorkDirectory: workDirectory,
      },
      null,
      2,
    )}\n`,
    "utf8",
  );
  console.error(`[g05-packaged-e2e] FAIL ${detail}`);
  process.exitCode = 1;
} finally {
  await stopProcess(launch);
  if (succeeded) await rm(workDirectory, { recursive: true, force: true });
  else log("failure", `retained workspace=${workDirectory}`);
}
