#!/usr/bin/env node

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

import { PackagedE2EClient } from "./packaged-e2e-driver.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const evidenceDirectory = path.join(root, "build", "e2e-evidence", "p9-g04");
const handshakeTimeoutMilliseconds = 600_000;
const rendererTimeoutMilliseconds = 75_000;
const processStopTimeoutMilliseconds = 8_000;
const visualPauseMilliseconds = Number.parseInt(
  process.env.KOYORI_IDE_RECOVERY_E2E_VISUAL_PAUSE_MS ?? "0",
  10,
);
const dryRun = process.argv.includes("--dry-run");

function log(stage, detail) {
  console.log(`[recovery-packaged-e2e] ${stage}: ${detail}`);
}

function hostEnvironment(overrides = {}) {
  const environment = { ...process.env, ...overrides };
  for (const name of ["GOOS", "GOARCH", "GOFLAGS", "CGO_ENABLED"]) {
    delete environment[name];
  }
  return environment;
}

function appendBounded(current, chunk, limit = 16 * 1024 * 1024) {
  const next = current + chunk;
  return next.length <= limit ? next : next.slice(next.length - limit);
}

function sha256Bytes(content) {
  return createHash("sha256").update(content).digest("hex");
}

async function sha256File(filePath) {
  return sha256Bytes(await readFile(filePath));
}

async function wailsPin() {
  const goMod = await readFile(path.join(root, "go.mod"), "utf8");
  const match = goMod.match(/^\s*github\.com\/wailsapp\/wails\/v3\s+(v\S+)/m);
  assert(match, "github.com/wailsapp/wails/v3 is not pinned in go.mod");
  return match[1];
}

async function listFiles(directory, predicate) {
  const result = [];
  let entries;
  try {
    entries = await readdir(directory, { withFileTypes: true });
  } catch (error) {
    if (error?.code === "ENOENT") return result;
    throw error;
  }
  for (const entry of entries) {
    const fullPath = path.join(directory, entry.name);
    if (entry.isDirectory())
      result.push(...(await listFiles(fullPath, predicate)));
    else if (entry.isFile() && predicate(fullPath)) result.push(fullPath);
  }
  return result;
}

async function sourceFingerprint() {
  const explicit = [
    "go.mod",
    "go.sum",
    "main.go",
    "bootstrap_services.go",
    "Taskfile.yml",
    "build/Taskfile.yml",
    "build/config.yml",
    "frontend/package.json",
    "frontend/package-lock.json",
    "frontend/src/App.vue",
    "frontend/src/main.ts",
    "frontend/src/e2e/recoveryProbe.ts",
    "frontend/src/router/index.ts",
    "frontend/src/stores/editor.ts",
    "frontend/src/stores/recovery.ts",
    "frontend/src/stores/workspaceStore.ts",
    "frontend/src/components/common/FocusTrapDialog.vue",
    "frontend/src/components/common/ModalOverlay.vue",
    "frontend/src/components/common/ModalOverlay.test.ts",
    "frontend/src/components/modals/RecoveryDialog.vue",
    "frontend/src/components/layout/TitleBar.vue",
    "frontend/src/api/platform.ts",
    "frontend/bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/recoveryservice.ts",
    "frontend/bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/windowservice.ts",
    "frontend/bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/projectservice.ts",
    "frontend/bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/models.ts",
    "scripts/recovery-packaged-e2e.mjs",
    "scripts/wails-bindings.manifest.json",
  ].map((relativePath) => path.join(root, relativePath));
  const discovered = [
    ...(await listFiles(
      path.join(root, "services"),
      (filePath) =>
        path.basename(filePath).startsWith("recovery") &&
        filePath.endsWith(".go"),
    )),
    ...(await listFiles(path.join(root, "internal", "e2e"), (filePath) =>
      filePath.endsWith(".go"),
    )),
  ];
  const files = [...new Set([...explicit, ...discovered])].sort();
  const hash = createHash("sha256");
  const manifest = [];
  for (const filePath of files) {
    const content = await readFile(filePath);
    const relativePath = path.relative(root, filePath).replaceAll("\\", "/");
    const digest = sha256Bytes(content);
    manifest.push({
      path: relativePath,
      sha256: digest,
      bytes: content.length,
    });
    hash.update(relativePath);
    hash.update("\0");
    hash.update(content);
    hash.update("\0");
  }
  return { sha256: hash.digest("hex"), files: manifest };
}

function gitCommit() {
  const result = spawnSync("git", ["rev-parse", "HEAD"], {
    cwd: root,
    encoding: "utf8",
    windowsHide: true,
  });
  return result.status === 0 ? result.stdout.trim() : null;
}

async function delay(milliseconds) {
  await new Promise((resolve) => setTimeout(resolve, milliseconds));
}

function spawnCaptured(command, args, options = {}) {
  let output = "";
  const child = spawn(command, args, {
    cwd: root,
    stdio: ["ignore", "pipe", "pipe"],
    windowsHide: true,
    ...options,
  });
  child.on("error", (error) => {
    child.spawnError = error;
  });
  child.stdout.on("data", (chunk) => {
    output = appendBounded(output, chunk.toString());
  });
  child.stderr.on("data", (chunk) => {
    output = appendBounded(output, chunk.toString());
  });
  return { child, output: () => output };
}

async function waitForExit(child, timeoutMilliseconds) {
  if (child.exitCode !== null || child.signalCode !== null) return;
  await Promise.race([
    new Promise((resolve) => child.once("exit", resolve)),
    delay(timeoutMilliseconds),
  ]);
}

async function stopProcessTree(launch) {
  if (
    !launch?.child?.pid ||
    launch.child.exitCode !== null ||
    launch.child.signalCode !== null
  ) {
    if (launch) await launch.flushLog();
    return;
  }
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
  await waitForExit(launch.child, processStopTimeoutMilliseconds);
  if (
    launch.child.exitCode === null &&
    launch.child.signalCode === null &&
    process.platform !== "win32"
  ) {
    try {
      process.kill(-launch.child.pid, "SIGKILL");
    } catch (error) {
      if (error?.code !== "ESRCH") throw error;
    }
  }
  await launch.flushLog();
}

async function forceTerminate(launch, reason) {
  assert(
    launch?.child?.pid,
    "force terminate requires a running packaged process",
  );
  const pid = launch.child.pid;
  const startedAt = new Date().toISOString();
  let command;
  let result;
  if (process.platform === "win32") {
    command = `taskkill /PID ${pid} /T /F`;
    result = spawnSync("taskkill", ["/PID", String(pid), "/T", "/F"], {
      encoding: "utf8",
      windowsHide: true,
    });
    if (result.status !== 0) {
      throw new Error(`${command} failed: ${result.stderr || result.stdout}`);
    }
  } else {
    command = `SIGKILL process group ${pid}`;
    try {
      process.kill(-pid, "SIGKILL");
      result = { status: 0, signal: "SIGKILL", stdout: "", stderr: "" };
    } catch (error) {
      if (error?.code !== "ESRCH") throw error;
      result = {
        status: 0,
        signal: "SIGKILL",
        stdout: "already exited",
        stderr: "",
      };
    }
  }
  await waitForExit(launch.child, processStopTimeoutMilliseconds);
  assert(
    launch.child.exitCode !== null || launch.child.signalCode !== null,
    `packaged process ${pid} remained alive after forced termination`,
  );
  await launch.flushLog();
  return {
    reason,
    pid,
    command,
    commandExitCode: result.status,
    commandSignal: result.signal ?? null,
    processExitCode: launch.child.exitCode,
    processSignal: launch.child.signalCode,
    startedAt,
    completedAt: new Date().toISOString(),
  };
}

async function runCommand(command, args, environment, logPath, cwd = root) {
  const launch = spawnCaptured(command, args, { cwd, env: environment });
  const completed = await new Promise((resolve, reject) => {
    launch.child.once("error", reject);
    launch.child.once("exit", (code, signal) => resolve({ code, signal }));
  });
  await writeFile(logPath, launch.output(), "utf8");
  if (completed.code !== 0) {
    throw new Error(
      `${command} ${args.join(" ")} failed (code=${completed.code}, signal=${completed.signal})`,
    );
  }
}

async function preparePinnedCLI(workDirectory, pin) {
  const providedExecutable = process.env.KOYORI_IDE_RECOVERY_E2E_WAILS3?.trim();
  if (providedExecutable) {
    const executable = path.resolve(providedExecutable);
    const info = await stat(executable);
    assert(info.isFile(), `provided Wails CLI is not a file: ${executable}`);
    const version = spawnSync(executable, ["version"], {
      encoding: "utf8",
      windowsHide: true,
    });
    const reportedVersion = `${version.stdout}${version.stderr}`.trim();
    if (version.status !== 0 || reportedVersion !== pin) {
      throw new Error(
        `provided Wails CLI reported ${JSON.stringify(reportedVersion)}, want exactly ${pin}`,
      );
    }
    return {
      directory: path.dirname(executable),
      executable,
      version: pin,
      sha256: await sha256File(executable),
      source: "provided-verified-path",
      sourcePath: executable,
      installLogs: [],
    };
  }

  const cliDirectory = path.join(workDirectory, "wails-cli");
  await mkdir(cliDirectory, { recursive: true });
  const executable = path.join(
    cliDirectory,
    process.platform === "win32" ? "wails3.exe" : "wails3",
  );
  const installLogs = [];
  let installError;
  for (let attempt = 1; attempt <= 3; attempt += 1) {
    const buildLog = path.join(
      evidenceDirectory,
      `wails-cli-build-attempt-${attempt}.log`,
    );
    installLogs.push(path.basename(buildLog));
    try {
      await runCommand(
        "go",
        ["install", `github.com/wailsapp/wails/v3/cmd/wails3@${pin}`],
        hostEnvironment({ GOBIN: cliDirectory }),
        buildLog,
      );
      installError = undefined;
      break;
    } catch (error) {
      installError = error;
      if (attempt < 3) await delay(attempt * 1_000);
    }
  }
  if (installError) throw installError;
  const version = spawnSync(executable, ["version"], {
    encoding: "utf8",
    windowsHide: true,
  });
  const reportedVersion = `${version.stdout}${version.stderr}`.trim();
  if (version.status !== 0 || reportedVersion !== pin) {
    throw new Error(
      `temporary Wails CLI reported ${JSON.stringify(reportedVersion)}, want exactly ${pin}`,
    );
  }
  return {
    directory: cliDirectory,
    executable,
    version: pin,
    sha256: await sha256File(executable),
    source: "go-install",
    sourcePath: null,
    installLogs,
  };
}

async function waitForJSON(filePath, child) {
  const deadline = Date.now() + handshakeTimeoutMilliseconds;
  while (Date.now() < deadline) {
    if (child.spawnError)
      throw new Error(`process spawn failed: ${child.spawnError.message}`);
    if (child.exitCode !== null || child.signalCode !== null) {
      throw new Error(
        `process exited before handshake (code=${child.exitCode}, signal=${child.signalCode})`,
      );
    }
    try {
      return JSON.parse(await readFile(filePath, "utf8"));
    } catch (error) {
      if (error?.code !== "ENOENT" && !(error instanceof SyntaxError))
        throw error;
    }
    await delay(100);
  }
  throw new Error(`timed out waiting for handshake ${filePath}`);
}

function executionEnvironment(cliDirectory, configDirectory, overrides = {}) {
  return hostEnvironment({
    PATH: `${cliDirectory}${path.delimiter}${process.env.PATH ?? ""}`,
    VITE_KOYORI_IDE_E2E_RECOVERY: "1",
    KOYORI_IDE_E2E: "1",
    APPDATA: configDirectory,
    LOCALAPPDATA: configDirectory,
    XDG_CONFIG_HOME: configDirectory,
    XDG_CACHE_HOME: path.join(configDirectory, "cache"),
    ...overrides,
  });
}

async function launchArtifact({
  artifact,
  cliDirectory,
  configDirectory,
  workDirectory,
  index,
}) {
  const token = randomBytes(32).toString("hex");
  const runId = randomBytes(32).toString("hex");
  const launchDirectory = path.join(workDirectory, `launch-${index}`);
  const handshakePath = path.join(launchDirectory, "handshake.json");
  const logPath = path.join(evidenceDirectory, `packaged-launch-${index}.log`);
  await mkdir(launchDirectory, { recursive: true });
  await rm(handshakePath, { force: true });
  const environment = executionEnvironment(cliDirectory, configDirectory, {
    KOYORI_IDE_E2E_TOKEN: token,
    KOYORI_IDE_E2E_HANDSHAKE: handshakePath,
    KOYORI_IDE_E2E_RUN_ID: runId,
  });
  const launch = spawnCaptured(artifact, [], {
    env: environment,
    windowsHide: false,
  });
  const flushLog = () => writeFile(logPath, launch.output(), "utf8");
  try {
    const handshake = await waitForJSON(handshakePath, launch.child);
    assert.equal(
      handshake.pid,
      launch.child.pid,
      "handshake PID does not match packaged process",
    );
    await delay(1_000);
    assert.equal(
      launch.child.exitCode,
      null,
      "packaged process exited during startup settle",
    );
    log(
      "launch",
      `index=${index} pid=${handshake.pid} endpoint=${handshake.url}`,
    );
    return {
      ...launch,
      client: new PackagedE2EClient({ url: handshake.url, token }),
      handshake,
      logPath,
      flushLog,
    };
  } catch (error) {
    await stopProcessTree({ ...launch, flushLog });
    throw error;
  }
}

async function rendererProbe(client, mode, fixture) {
  let timeoutID;
  try {
    const timeout = new Promise((_, reject) => {
      timeoutID = setTimeout(
        () =>
          reject(
            new Error(`timed out waiting for recovery renderer mode ${mode}`),
          ),
        rendererTimeoutMilliseconds,
      );
    });
    const result = await Promise.race([
      client.command("recovery-renderer-probe", {
        probeMode: mode,
        path: fixture.filePath,
        expected: fixture.initialDisk,
        crashContent: fixture.crashContent,
        pendingContent: fixture.pendingContent,
      }),
      timeout,
    ]);
    assert.equal(
      result.ok,
      true,
      result.error ?? `renderer probe ${mode} failed`,
    );
    return result;
  } finally {
    clearTimeout(timeoutID);
  }
}

async function journalSnapshot(configDirectory) {
  const recoveryRoot = path.join(configDirectory, "koyori-ide", "recovery");
  const files = await listFiles(recoveryRoot, (filePath) =>
    filePath.endsWith(".json"),
  );
  const records = [];
  const otherJSON = [];
  for (const filePath of files.sort()) {
    const content = await readFile(filePath);
    let parsed;
    try {
      parsed = JSON.parse(content.toString("utf8"));
    } catch {
      parsed = null;
    }
    const entry = {
      path: path.relative(configDirectory, filePath).replaceAll("\\", "/"),
      sha256: sha256Bytes(content),
      bytes: content.length,
    };
    if (
      parsed &&
      typeof parsed.path === "string" &&
      typeof parsed.content === "string"
    ) {
      records.push({
        ...entry,
        workspacePath: parsed.path,
        windowId: parsed.windowId,
        content: parsed.content,
        baselineHash: parsed.baselineHash,
      });
    } else {
      otherJSON.push(entry);
    }
  }
  return { recoveryRoot, records, otherJSON };
}

async function captureWindowScreenshot(pid, outputPath) {
  if (process.platform !== "win32") {
    throw new Error(
      "P9-G04 packaged screenshot capture is currently implemented for Windows only",
    );
  }
  const script = String.raw`
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new()
Add-Type -AssemblyName System.Drawing
Add-Type @"
using System;
using System.Runtime.InteropServices;
public struct GugaRect { public int Left; public int Top; public int Right; public int Bottom; }
public static class GugaCaptureNative {
  public delegate bool EnumWindowsProc(IntPtr hWnd, IntPtr lParam);
  [DllImport("user32.dll")] public static extern bool EnumWindows(EnumWindowsProc callback, IntPtr lParam);
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr hWnd, out GugaRect rect);
  [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr hWnd);
  [DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr hWnd, out uint processId);

  public static IntPtr FindVisibleWindow(int processId) {
    IntPtr largest = IntPtr.Zero;
    long largestArea = 0;
    EnumWindows(delegate(IntPtr handle, IntPtr _) {
      uint owner;
      GetWindowThreadProcessId(handle, out owner);
      if (owner != (uint)processId || !IsWindowVisible(handle)) return true;
      GugaRect rect;
      if (!GetWindowRect(handle, out rect)) return true;
      long width = Math.Max(0, rect.Right - rect.Left);
      long height = Math.Max(0, rect.Bottom - rect.Top);
      long area = width * height;
      if (area > largestArea) {
        largest = handle;
        largestArea = area;
      }
      return true;
    }, IntPtr.Zero);
    return largest;
  }
}
"@
$capturePID = [int]$env:KOYORI_IDE_CAPTURE_PID
$capturePath = $env:KOYORI_IDE_CAPTURE_PATH
$deadline = [DateTime]::UtcNow.AddSeconds(15)
$handle = [IntPtr]::Zero
while ([DateTime]::UtcNow -lt $deadline) {
  [void](Get-Process -Id $capturePID -ErrorAction Stop)
  $handle = [GugaCaptureNative]::FindVisibleWindow($capturePID)
  if ($handle -ne [IntPtr]::Zero) { break }
  Start-Sleep -Milliseconds 100
}
if ($handle -eq [IntPtr]::Zero) { throw "packaged process has no main window handle" }
$rect = New-Object GugaRect
if (-not [GugaCaptureNative]::GetWindowRect($handle, [ref]$rect)) { throw "GetWindowRect failed" }
$width = $rect.Right - $rect.Left
$height = $rect.Bottom - $rect.Top
if ($width -lt 400 -or $height -lt 300) { throw "unexpected window dimensions $($width)x$($height)" }
$bitmap = New-Object System.Drawing.Bitmap($width, $height)
$graphics = [System.Drawing.Graphics]::FromImage($bitmap)
try {
  $graphics.CopyFromScreen($rect.Left, $rect.Top, 0, 0, $bitmap.Size)
  $bitmap.Save($capturePath, [System.Drawing.Imaging.ImageFormat]::Png)
  $colours = New-Object 'System.Collections.Generic.HashSet[int]'
  $stepX = [Math]::Max(1, [int]($width / 32))
  $stepY = [Math]::Max(1, [int]($height / 24))
  for ($x = 0; $x -lt $width; $x += $stepX) {
    for ($y = 0; $y -lt $height; $y += $stepY) {
      [void]$colours.Add($bitmap.GetPixel($x, $y).ToArgb())
    }
  }
  [pscustomobject]@{ width = $width; height = $height; sampledUniqueColours = $colours.Count } | ConvertTo-Json -Compress
} finally {
  $graphics.Dispose()
  $bitmap.Dispose()
}
`;
  const result = spawnSync(
    "powershell.exe",
    ["-NoProfile", "-NonInteractive", "-Command", script],
    {
      encoding: "utf8",
      windowsHide: true,
      env: hostEnvironment({
        KOYORI_IDE_CAPTURE_PID: String(pid),
        KOYORI_IDE_CAPTURE_PATH: outputPath,
      }),
    },
  );
  if (result.status !== 0) {
    throw new Error(
      `window screenshot failed: ${result.stderr || result.stdout}`,
    );
  }
  const metadata = JSON.parse(result.stdout.trim().split(/\r?\n/).at(-1));
  const info = await stat(outputPath);
  assert(info.size > 10_000, "window screenshot is unexpectedly small");
  assert(
    metadata.sampledUniqueColours > 20,
    "window screenshot appears blank or one-colour",
  );
  return {
    file: path.basename(outputPath),
    sha256: await sha256File(outputPath),
    bytes: info.size,
    captureMethod: "EnumWindows/GetWindowRect/CopyFromScreen",
    ...metadata,
  };
}

async function verifyProductionRendererHookAbsent(environment) {
  const logPath = path.join(evidenceDirectory, "production-frontend-build.log");
  const productionEnvironment = { ...environment };
  delete productionEnvironment.VITE_KOYORI_IDE_E2E_RECOVERY;
  delete productionEnvironment.KOYORI_IDE_E2E;
  const command = process.platform === "win32" ? "cmd.exe" : "npm";
  const args =
    process.platform === "win32"
      ? ["/d", "/s", "/c", "npm.cmd run build"]
      : ["run", "build"];
  await runCommand(
    command,
    args,
    productionEnvironment,
    logPath,
    path.join(root, "frontend"),
  );
  const distFiles = await listFiles(
    path.join(root, "frontend", "dist"),
    (filePath) => filePath.endsWith(".js") || filePath.endsWith(".html"),
  );
  const forbidden = ["__koyoriIdeRunRecoveryProbe", recoveryResultEventMarker];
  for (const filePath of distFiles) {
    const content = await readFile(filePath, "utf8");
    for (const marker of forbidden) {
      assert(
        !content.includes(marker),
        `production frontend contains recovery E2E marker ${marker}`,
      );
    }
  }
  await rm(path.join(root, "frontend", "dist"), {
    recursive: true,
    force: true,
  });
  return {
    checkedFiles: distFiles.length,
    forbiddenMarkers: forbidden,
    buildLog: path.basename(logPath),
    buildLogSha256: await sha256File(logPath),
    distRemovedBeforeE2EBuild: true,
  };
}

const recoveryResultEventMarker = "e2e:recovery-result";

async function removeTemporaryDirectory(directory) {
  let lastError;
  for (let attempt = 0; attempt < 20; attempt += 1) {
    try {
      await rm(directory, { recursive: true, force: true });
      return;
    } catch (error) {
      lastError = error;
      if (error?.code !== "EBUSY" && error?.code !== "EPERM") throw error;
      await delay(250);
    }
  }
  throw lastError;
}

if (dryRun) {
  assert.equal(await wailsPin(), "v3.0.0-alpha2.111");
  const source = await readFile(fileURLToPath(import.meta.url), "utf8");
  for (const required of [
    "prepare-crash",
    "pending-check",
    "resolve-save",
    "native-window-close-probe",
    "taskkill",
    "APPDATA",
    "LOCALAPPDATA",
    "sourceFingerprint",
  ]) {
    assert(
      source.includes(required),
      `recovery packaged driver is missing ${required}`,
    );
  }
  log(
    "dry-run",
    "source plan validated; no packaged process was launched and evidence remains U",
  );
  process.exit(0);
}

await mkdir(evidenceDirectory, { recursive: true });
const passPath = path.join(evidenceDirectory, "recovery-packaged-runtime.json");
const failurePath = path.join(
  evidenceDirectory,
  "recovery-packaged-runtime.failure.json",
);
await Promise.all([
  rm(passPath, { force: true }),
  rm(failurePath, { force: true }),
]);
const workDirectory = await mkdtemp(
  path.join(os.tmpdir(), "koyori-ide-recovery-e2e-"),
);
const startedAt = new Date().toISOString();
let launch;
let succeeded = false;

try {
  const configDirectory = path.join(workDirectory, "config");
  const workspace = path.join(workDirectory, "workspace-a");
  const otherWorkspace = path.join(workDirectory, "workspace-b");
  await Promise.all([
    mkdir(configDirectory, { recursive: true }),
    mkdir(workspace, { recursive: true }),
    mkdir(otherWorkspace, { recursive: true }),
  ]);
  const fixture = {
    workspace,
    otherWorkspace,
    filePath: path.join(workspace, "recovery.ts"),
    initialDisk: "export const diskVersion = 'original';\n",
    crashContent:
      "export const diskVersion = 'original';\n// unsaved before first crash\n",
    pendingContent:
      "export const diskVersion = 'original';\n// edited while recovery was pending\n",
  };
  await writeFile(fixture.filePath, fixture.initialDisk, "utf8");

  const pin = await wailsPin();
  const cli = await preparePinnedCLI(workDirectory, pin);
  const baseEnvironment = executionEnvironment(cli.directory, configDirectory);
  const productionHookAbsence =
    await verifyProductionRendererHookAbsent(baseEnvironment);
  const source = await sourceFingerprint();
  const commit = gitCommit();
  log("source", `fingerprint=${source.sha256} git=${commit ?? "unavailable"}`);

  const buildLogPath = path.join(
    evidenceDirectory,
    "recovery-packaged-build.log",
  );
  await runCommand(
    cli.executable,
    ["build", "-tags", "e2e"],
    baseEnvironment,
    buildLogPath,
  );
  const artifact = path.join(
    root,
    "bin",
    process.platform === "win32" ? "koyori-ide.exe" : "koyori-ide",
  );
  const artifactInfo = await stat(artifact);
  assert(
    artifactInfo.size > 1024 * 1024,
    "packaged artifact is unexpectedly small",
  );
  const artifactSha256 = await sha256File(artifact);

  launch = await launchArtifact({
    artifact,
    cliDirectory: cli.directory,
    configDirectory,
    workDirectory,
    index: 1,
  });
  await launch.client.command("open-workspace", { workspace });
  const firstRenderer = await rendererProbe(
    launch.client,
    "prepare-crash",
    fixture,
  );
  const firstJournal = await journalSnapshot(configDirectory);
  assert.equal(
    firstJournal.records.length,
    1,
    "first crash preparation did not create exactly one journal record",
  );
  assert.equal(firstJournal.records[0].content, fixture.crashContent);
  const firstScreenshot = await captureWindowScreenshot(
    launch.child.pid,
    path.join(evidenceDirectory, "recovery-before-first-kill.png"),
  );
  const firstKill = await forceTerminate(
    launch,
    "crash after a dirty Monaco edit",
  );
  launch = null;

  launch = await launchArtifact({
    artifact,
    cliDirectory: cli.directory,
    configDirectory,
    workDirectory,
    index: 2,
  });
  await launch.client.command("open-workspace", { workspace });
  const pendingRenderer = await rendererProbe(
    launch.client,
    "pending-check",
    fixture,
  );
  assert.equal(pendingRenderer.phase, "pending");
  assert.equal(pendingRenderer.afterAutoSave, fixture.initialDisk);
  assert.equal(pendingRenderer.afterBlur, fixture.initialDisk);

  const guardResult = await launch.client.command("recovery-guard-probe", {
    workspace: otherWorkspace,
    windowId: pendingRenderer.originalWindowId,
    path: fixture.filePath,
  });
  assert.equal(Object.keys(guardResult.rejections).length, 7);
  const nativeClose = await launch.client.command("native-window-close-probe");
  assert.equal(nativeClose.hookInvoked, true);
  const aliveAfterNativeClose = await launch.client.command("recovery-state");
  assert.equal(aliveAfterNativeClose.phase, "pending");
  assert.equal(await readFile(fixture.filePath, "utf8"), fixture.initialDisk);

  const secondJournal = await journalSnapshot(configDirectory);
  assert.equal(
    secondJournal.records.length,
    2,
    "pending edit was not isolated from the first crash snapshot",
  );
  const preservedOriginal = secondJournal.records.find(
    (record) => record.windowId === pendingRenderer.originalWindowId,
  );
  assert(
    preservedOriginal,
    "original crash snapshot disappeared during pending guards",
  );
  assert.equal(
    preservedOriginal.sha256,
    firstJournal.records[0].sha256,
    "original crash snapshot changed during pending autosave/blur/close checks",
  );
  assert(
    secondJournal.records.some(
      (record) => record.content === fixture.pendingContent,
    ),
    "pending-session independent journal was not persisted",
  );
  const pendingScreenshot = await captureWindowScreenshot(
    launch.child.pid,
    path.join(evidenceDirectory, "recovery-pending.png"),
  );
  if (Number.isFinite(visualPauseMilliseconds) && visualPauseMilliseconds > 0) {
    log(
      "visual-pause",
      `pending window remains open for ${visualPauseMilliseconds}ms`,
    );
    await delay(visualPauseMilliseconds);
  }
  const secondKill = await forceTerminate(
    launch,
    "second crash while recovery remained pending",
  );
  launch = null;

  launch = await launchArtifact({
    artifact,
    cliDirectory: cli.directory,
    configDirectory,
    workDirectory,
    index: 3,
  });
  await launch.client.command("open-workspace", { workspace });
  const resolvedRenderer = await rendererProbe(
    launch.client,
    "resolve-save",
    fixture,
  );
  assert.equal(resolvedRenderer.recoveredCount, 2);
  assert.equal(resolvedRenderer.undoVerified, true);
  assert.equal(resolvedRenderer.manualSave, true);
  assert.equal(resolvedRenderer.rescanCount, 0);
  assert.equal(resolvedRenderer.dialogVisible, false);
  assert.equal(
    await readFile(fixture.filePath, "utf8"),
    fixture.pendingContent,
  );
  const finalState = await launch.client.command("recovery-state");
  assert.equal(finalState.phase, "resolved");
  const finalJournal = await journalSnapshot(configDirectory);
  assert.equal(
    finalJournal.records.length,
    0,
    "recovery records remained after explicit completion and manual save",
  );
  const resolvedScreenshot = await captureWindowScreenshot(
    launch.child.pid,
    path.join(evidenceDirectory, "recovery-resolved.png"),
  );

  await launch.flushLog();
  const applicationLogs = [];
  for (let index = 1; index <= 3; index += 1) {
    const logPath = path.join(
      evidenceDirectory,
      `packaged-launch-${index}.log`,
    );
    applicationLogs.push({
      file: path.basename(logPath),
      sha256: await sha256File(logPath),
      bytes: (await stat(logPath)).size,
    });
  }
  const evidence = {
    schemaVersion: 1,
    goal: "P9-G04",
    status: "passed",
    evidenceLevel: "P",
    startedAt,
    completedAt: new Date().toISOString(),
    platform: `${process.platform}/${process.arch}`,
    osRelease: os.release(),
    nodeVersion: process.version,
    wailsVersion: cli.version,
    wailsCLISha256: cli.sha256,
    wailsCLI: {
      source: cli.source,
      sourcePath: cli.sourcePath,
      installLogs: cli.installLogs,
    },
    gitCommit: commit,
    gitMetadataAvailable: commit !== null,
    sourceFingerprintSha256: source.sha256,
    sourceFingerprintFiles: source.files,
    artifact: path.relative(root, artifact).replaceAll("\\", "/"),
    artifactSha256,
    artifactBytes: artifactInfo.size,
    buildTags: ["desktop", "production", "e2e"],
    buildLog: path.basename(buildLogPath),
    buildLogSha256: await sha256File(buildLogPath),
    productionHookAbsence,
    isolatedConfig: {
      appData: configDirectory,
      localAppData: configDirectory,
      xdgConfigHome: configDirectory,
      xdgCacheHome: path.join(configDirectory, "cache"),
      realUserConfigAndCacheUntouchedByHarness: true,
    },
    fixture: {
      workspace,
      otherWorkspace,
      filePath: fixture.filePath,
      initialDiskSha256: sha256Bytes(fixture.initialDisk),
      crashContentSha256: sha256Bytes(fixture.crashContent),
      pendingContentSha256: sha256Bytes(fixture.pendingContent),
      finalDiskSha256: sha256Bytes(await readFile(fixture.filePath)),
    },
    launches: [
      {
        index: 1,
        pid: firstKill.pid,
        startedAt: firstRenderer.runId ? firstKill.startedAt : null,
      },
      { index: 2, pid: secondKill.pid, startedAt: secondKill.startedAt },
      {
        index: 3,
        pid: launch.child.pid,
        startedAt: launch.handshake.startedAt,
      },
    ],
    forcedTermination: [firstKill, secondKill],
    renderer: {
      firstLaunch: firstRenderer,
      pendingLaunch: pendingRenderer,
      resolvedLaunch: resolvedRenderer,
    },
    guards: {
      ...guardResult,
      nativeWindowClosingHook: nativeClose,
      processAliveAfterNativeClose: aliveAfterNativeClose.phase === "pending",
      diskUnchangedWhilePending: true,
      originalSnapshotSha256Before: firstJournal.records[0].sha256,
      originalSnapshotSha256After: preservedOriginal.sha256,
    },
    repeatedCrash: {
      firstJournalRecordCount: firstJournal.records.length,
      secondJournalRecordCount: secondJournal.records.length,
      finalJournalRecordCount: finalJournal.records.length,
      originalSnapshotPreservedByteForByte:
        preservedOriginal.sha256 === firstJournal.records[0].sha256,
    },
    screenshots: [firstScreenshot, pendingScreenshot, resolvedScreenshot],
    applicationLogs,
    limitations:
      commit === null
        ? [
            "The workspace has an empty .git directory. This evidence records a deterministic source-input fingerprint and does not claim a commit.",
          ]
        : [],
  };
  await writeFile(passPath, `${JSON.stringify(evidence, null, 2)}\n`, "utf8");
  await rm(failurePath, { force: true });
  succeeded = true;
  log(
    "pass",
    `artifact=${artifactSha256} evidence=${path.relative(root, passPath)}`,
  );
} catch (error) {
  const detail =
    error instanceof Error ? (error.stack ?? error.message) : String(error);
  await writeFile(
    failurePath,
    `${JSON.stringify(
      {
        schemaVersion: 1,
        goal: "P9-G04",
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
  console.error(`[recovery-packaged-e2e] FAIL ${detail}`);
  throw error;
} finally {
  await stopProcessTree(launch);
  if (succeeded) {
    const resolved = path.resolve(workDirectory);
    if (
      path.dirname(resolved) !== path.resolve(os.tmpdir()) ||
      !path.basename(resolved).startsWith("koyori-ide-recovery-e2e-")
    ) {
      throw new Error(
        `refusing to remove unexpected E2E directory ${resolved}`,
      );
    }
    await removeTemporaryDirectory(resolved);
  } else {
    console.error(
      `[recovery-packaged-e2e] retained failure workspace: ${workDirectory}`,
    );
  }
}
