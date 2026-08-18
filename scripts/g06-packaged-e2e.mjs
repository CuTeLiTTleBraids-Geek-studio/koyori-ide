#!/usr/bin/env node

import assert from "node:assert/strict";
import { createHash, randomBytes } from "node:crypto";
import { spawn, spawnSync } from "node:child_process";
import { mkdir, mkdtemp, readFile, readdir, rm, stat, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { PackagedE2EClient } from "./packaged-e2e-driver.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const evidenceDir = path.join(root, "build", "e2e-evidence", "p9-g06");
const cliRelative = path.join("build", "e2e-evidence", "p9-g06", "wails-cli", process.platform === "win32" ? "wails3.exe" : "wails3");
const startupTimeout = 600_000;
const stopTimeout = 8_000;
const dryRun = process.argv.includes("--dry-run");

function sha256(value) { return createHash("sha256").update(value).digest("hex"); }
async function sha256File(file) { return sha256(await readFile(file)); }
function hostEnvironment(overrides = {}) {
  const env = { ...process.env, ...overrides };
  for (const name of ["GOOS", "GOARCH", "GOFLAGS", "CGO_ENABLED"]) delete env[name];
  return env;
}
function spawnCaptured(command, args, options = {}) {
  let output = "";
  const child = spawn(command, args, { cwd: root, stdio: ["ignore", "pipe", "pipe"], windowsHide: false, ...options });
  child.on("error", (error) => { child.spawnError = error; });
  child.stdout.on("data", (chunk) => { output += chunk.toString(); });
  child.stderr.on("data", (chunk) => { output += chunk.toString(); });
  return { child, output: () => output };
}
async function runCommand(command, args, env, logPath, cwd = root) {
  const launch = spawnCaptured(command, args, { cwd, env });
  const result = await new Promise((resolve, reject) => {
    launch.child.once("error", reject);
    launch.child.once("exit", (code, signal) => resolve({ code, signal }));
  });
  const output = launch.output();
  await writeFile(logPath, output, "utf8");
  if (result.code !== 0) throw new Error(`${command} ${args.join(" ")} failed (code=${result.code}, signal=${result.signal})`);
  return { ...result, output };
}
async function wait(ms) { await new Promise((resolve) => setTimeout(resolve, ms)); }
async function pinnedVersion() {
  const mod = await readFile(path.join(root, "go.mod"), "utf8");
  const match = mod.match(/^\s*github\.com\/wailsapp\/wails\/v3\s+(v\S+)/m);
  assert(match, "Wails v3 is not pinned");
  return match[1];
}
async function verifiedCLI(workDirectory, pin) {
  const supplied = process.env.KOYORI_IDE_G06_E2E_WAILS3?.trim();
  const candidate = supplied ? path.resolve(supplied) : path.resolve(root, cliRelative);
  try {
    const info = await stat(candidate);
    assert(info.isFile(), `Wails CLI is not a file: ${candidate}`);
    const version = spawnSync(candidate, ["version"], { encoding: "utf8", windowsHide: true });
    assert.equal(version.status, 0, `${version.stdout}${version.stderr}`);
    assert.equal(`${version.stdout}${version.stderr}`.trim(), pin);
    return { executable: candidate, directory: path.dirname(candidate), version: pin, source: supplied ? "provided-verified-path" : "existing-pinned-evidence-cli", sha256: await sha256File(candidate) };
  } catch (error) {
    if (supplied) throw error;
  }
  const directory = path.join(workDirectory, "wails-cli");
  await mkdir(directory, { recursive: true });
  const executable = path.join(directory, process.platform === "win32" ? "wails3.exe" : "wails3");
  const installLog = path.join(evidenceDir, "g06-wails-cli-install.log");
  await runCommand("go", ["install", `github.com/wailsapp/wails/v3/cmd/wails3@${pin}`], hostEnvironment({ GOBIN: directory }), installLog);
  const version = spawnSync(executable, ["version"], { encoding: "utf8", windowsHide: true });
  assert.equal(`${version.stdout}${version.stderr}`.trim(), pin);
  return { executable, directory, version: pin, source: "go-install", sha256: await sha256File(executable), installLog: path.basename(installLog) };
}
async function waitForHandshake(file, child) {
  const deadline = Date.now() + startupTimeout;
  while (Date.now() < deadline) {
    if (child.spawnError) throw child.spawnError;
    if (child.exitCode !== null || child.signalCode !== null) throw new Error(`artifact exited before handshake (${child.exitCode})`);
    try { return JSON.parse(await readFile(file, "utf8")); } catch (error) {
      if (error?.code !== "ENOENT" && !(error instanceof SyntaxError)) throw error;
    }
    await wait(100);
  }
  throw new Error(`timed out waiting for handshake ${file}`);
}
async function stopProcess(launch) {
  if (!launch?.child || launch.child.exitCode !== null) return;
  if (process.platform === "win32") spawnSync("taskkill", ["/PID", String(launch.child.pid), "/T", "/F"], { encoding: "utf8", windowsHide: true });
  else { try { process.kill(-launch.child.pid, "SIGTERM"); } catch (error) { if (error?.code !== "ESRCH") throw error; } }
  await Promise.race([new Promise((resolve) => launch.child.once("exit", resolve)), wait(stopTimeout)]);
  await launch.flushLog?.();
}
async function launchArtifact(artifact, configDirectory, workDirectory) {
  const token = randomBytes(32).toString("hex");
  const launchDirectory = path.join(workDirectory, "launch");
  const handshakePath = path.join(launchDirectory, "handshake.json");
  const logPath = path.join(evidenceDir, "g06-packaged-launch.log");
  await mkdir(launchDirectory, { recursive: true });
  const launch = spawnCaptured(artifact, [], { env: hostEnvironment({
    KOYORI_IDE_E2E: "1",
    KOYORI_IDE_E2E_TOKEN: token,
    KOYORI_IDE_E2E_HANDSHAKE: handshakePath,
    XDG_CONFIG_HOME: configDirectory,
    APPDATA: configDirectory,
    LOCALAPPDATA: configDirectory,
    XDG_CACHE_HOME: path.join(configDirectory, "cache"),
  }) });
  launch.flushLog = () => writeFile(logPath, launch.output(), "utf8");
  try {
    const handshake = await waitForHandshake(handshakePath, launch.child);
    assert.equal(handshake.pid, launch.child.pid);
    await wait(1_500);
    assert.equal(launch.child.exitCode, null);
    return { ...launch, handshake, client: new PackagedE2EClient({ url: handshake.url, token }) };
  } catch (error) {
    await stopProcess({ ...launch, flushLog: launch.flushLog });
    throw error;
  }
}
async function productionHookAbsence() {
  const logPath = path.join(evidenceDir, "g06-production-frontend-build.log");
  const command = process.platform === "win32" ? "cmd.exe" : "npm";
  const args = process.platform === "win32" ? ["/d", "/s", "/c", "npm.cmd run build"] : ["run", "build"];
  await runCommand(command, args, hostEnvironment(), logPath, path.join(root, "frontend"));
  const dist = path.join(root, "frontend", "dist");
  const files = [];
  async function scan(directory) {
    let entries;
    try { entries = await readdir(directory, { withFileTypes: true }); } catch { return; }
    for (const entry of entries) {
      const full = path.join(directory, entry.name);
      if (entry.isDirectory()) await scan(full);
      else if (entry.isFile() && /\.(js|html)$/.test(entry.name)) files.push(full);
    }
  }
  await scan(dist);
  // The production resolver must retain the query-key parser. Only the
  // opt-in probe hook and its event are forbidden from the normal bundle.
  const forbiddenMarkers = ["__koyoriIdeRunG06RuntimeRoleProbe", "e2e:g06-runtime-role-result"];
  const violations = [];
  for (const file of files) {
    const content = await readFile(file, "utf8");
    if (forbiddenMarkers.some((marker) => content.includes(marker))) violations.push(path.relative(root, file));
  }
  await rm(dist, { recursive: true, force: true });
  assert.deepEqual(violations, []);
  return { checkedFiles: files.length, forbiddenMarkers, buildLog: path.basename(logPath), buildLogSha256: await sha256File(logPath) };
}
async function captureWindow(pid, role, outputPath) {
  if (process.platform !== "win32") throw new Error("G06 packaged capture requires Windows");
  const script = String.raw`
Add-Type -AssemblyName System.Drawing
Add-Type @"
using System; using System.Runtime.InteropServices; using System.Text;
public struct GugaRect { public int Left; public int Top; public int Right; public int Bottom; }
public static class GugaWindowCapture { public delegate bool EnumWindowsProc(IntPtr h, IntPtr l);
[DllImport("user32.dll")] public static extern bool EnumWindows(EnumWindowsProc c, IntPtr l);
[DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr h);
[DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr h, out uint p);
[DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern int GetWindowText(IntPtr h, StringBuilder s, int m);
[DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr h, out GugaRect r);
public static IntPtr Find(int pid, string role) { IntPtr found=IntPtr.Zero; EnumWindows((h,_)=>{ uint owner; GetWindowThreadProcessId(h,out owner); if(owner!=(uint)pid||!IsWindowVisible(h))return true; var s=new StringBuilder(256);GetWindowText(h,s,s.Capacity);var t=s.ToString();if(role=="ai"&&t.IndexOf("koyori-ide AI",StringComparison.OrdinalIgnoreCase)<0)return true;if(role=="main"&&t.IndexOf("koyori-ide AI",StringComparison.OrdinalIgnoreCase)>=0)return true;found=h;return false;},IntPtr.Zero);return found;}}
"@
$processId = [int]$env:KOYORI_IDE_CAPTURE_PROCESS
$role = $env:KOYORI_IDE_CAPTURE_ROLE; $output = $env:KOYORI_IDE_CAPTURE_PATH
$deadline=[DateTime]::UtcNow.AddSeconds(30); $handle=[IntPtr]::Zero
while([DateTime]::UtcNow -lt $deadline){$handle=[GugaWindowCapture]::Find($processId,$role);if($handle -ne [IntPtr]::Zero){break};Start-Sleep -Milliseconds 100}
if($handle -eq [IntPtr]::Zero){throw "no visible $role window"};$rect=New-Object GugaRect
if(-not [GugaWindowCapture]::GetWindowRect($handle,[ref]$rect)){throw "GetWindowRect failed"};$w=$rect.Right-$rect.Left;$h=$rect.Bottom-$rect.Top
if($w -lt 400 -or $h -lt 300){throw "unexpected dimensions \${w}x\${h}"};$bitmap=New-Object System.Drawing.Bitmap($w,$h);$graphics=[System.Drawing.Graphics]::FromImage($bitmap)
try{$graphics.CopyFromScreen($rect.Left,$rect.Top,0,0,$bitmap.Size);$bitmap.Save($output,[System.Drawing.Imaging.ImageFormat]::Png);$colours=New-Object 'System.Collections.Generic.HashSet[int]';for($x=0;$x -lt $w;$x += [Math]::Max(1,[int]($w/32))){for($y=0;$y -lt $h;$y += [Math]::Max(1,[int]($h/24))){[void]$colours.Add($bitmap.GetPixel($x,$y).ToArgb())}};[pscustomobject]@{width=$w;height=$h;sampledUniqueColours=$colours.Count}|ConvertTo-Json -Compress}finally{$graphics.Dispose();$bitmap.Dispose()}
`;
  const result = spawnSync("powershell.exe", ["-NoProfile", "-NonInteractive", "-Command", script], { encoding: "utf8", windowsHide: true, env: hostEnvironment({ KOYORI_IDE_CAPTURE_PROCESS: String(pid), KOYORI_IDE_CAPTURE_ROLE: role, KOYORI_IDE_CAPTURE_PATH: outputPath }) });
  if (result.status !== 0) throw new Error(`capture ${role} failed: ${result.stderr || result.stdout}`);
  const metadata = JSON.parse(result.stdout.trim().split(/\r?\n/).at(-1));
  const info = await stat(outputPath); assert(info.size > 10_000); assert(metadata.sampledUniqueColours > 20);
  return { file: path.basename(outputPath), sha256: await sha256File(outputPath), bytes: info.size, ...metadata };
}
function processTree(pid) {
  if (process.platform === "win32") {
    const script = `$root=${pid}; Get-CimInstance Win32_Process | Where-Object { $_.ProcessId -eq $root -or $_.ParentProcessId -eq $root } | Select-Object ProcessId,ParentProcessId,Name | ConvertTo-Json -Compress`;
    const result = spawnSync("powershell.exe", ["-NoProfile", "-NonInteractive", "-Command", script], { encoding: "utf8", windowsHide: true });
    try { return JSON.parse(result.stdout.trim() || "[]"); } catch { return []; }
  }
  return [];
}
async function sourceFingerprint() {
  const files = ["main.go", "services/window_service.go", "services/runtime_role.go", "services/runtime_role_e2e.go", "internal/e2e/server.go", "frontend/src/main.ts", "frontend/src/App.vue", "frontend/src/runtimeRole.ts", "frontend/src/e2e/runtimeRoleProbe.ts", "scripts/g06-packaged-e2e.mjs"].sort();
  const hash = createHash("sha256"); const details = [];
  for (const file of files) { const content = await readFile(path.join(root, file)); const digest = sha256(content); details.push({ path: file, sha256: digest, bytes: content.length }); hash.update(file); hash.update("\0"); hash.update(digest); hash.update("\n"); }
  return { sha256: hash.digest("hex"), files: details };
}
function gitCommit() { const result = spawnSync("git", ["rev-parse", "HEAD"], { cwd: root, encoding: "utf8", windowsHide: true }); return result.status === 0 ? result.stdout.trim() : null; }

if (dryRun) { assert.equal(await pinnedVersion(), "v3.0.0-beta.8"); console.log("[g06-packaged-e2e] dry-run: no process launched; evidence remains U"); process.exit(0); }

await mkdir(evidenceDir, { recursive: true });
const evidencePath = path.join(evidenceDir, "g06-packaged-runtime.json");
const failurePath = path.join(evidenceDir, "g06-packaged-runtime.failure.json");
await Promise.all([rm(evidencePath, { force: true }), rm(failurePath, { force: true })]);
const workDirectory = await mkdtemp(path.join(os.tmpdir(), "koyori-ide-g06-e2e-"));
let launch; let succeeded = false; const startedAt = new Date().toISOString();
try {
  const configDirectory = path.join(workDirectory, "config"); await mkdir(configDirectory, { recursive: true });
  const pin = await pinnedVersion(); const cli = await verifiedCLI(workDirectory, pin); const production = await productionHookAbsence(); const source = await sourceFingerprint();
  const artifact = path.join(root, "bin", process.platform === "win32" ? "koyori-ide.exe" : "koyori-ide");
  const buildLog = path.join(evidenceDir, "g06-packaged-build.log");
  await runCommand(cli.executable, ["build", "-tags", "e2e"], hostEnvironment({ PATH: `${cli.directory}${path.delimiter}${process.env.PATH ?? ""}`, VITE_KOYORI_IDE_E2E_RUNTIME_ROLE: "1" }), buildLog);
  const artifactInfo = await stat(artifact); assert(artifactInfo.size > 1024 * 1024); const artifactSha256 = await sha256File(artifact);
  launch = await launchArtifact(artifact, configDirectory, workDirectory);
  const probe = await launch.client.command("g06-runtime-role-probe");
  assert.equal(probe.main.ok, true, probe.main.error ?? "main role probe failed");
  assert.equal(probe.aiFirst.ok, true, probe.aiFirst.error ?? "first AI role probe failed");
  assert.equal(probe.aiReopen.ok, true, probe.aiReopen.error ?? "reopened AI role probe failed");
  assert.equal(probe.main.role, "main"); assert.equal(probe.aiFirst.role, "ai"); assert.equal(probe.aiReopen.role, "ai");
  assert.equal(probe.main.forgedRole, "minimal"); assert.equal(probe.aiFirst.forgedRole, "minimal"); assert.equal(probe.aiReopen.forgedRole, "minimal");
  assert(probe.main.stages.includes("workflows"));
  for (const ai of [probe.aiFirst, probe.aiReopen]) for (const forbidden of ["debug-runtime", "test-explorer-runtime", "connectivity", "lsp", "plugins", "layout", "workflows"]) assert(!ai.stages.includes(forbidden), `AI ran ${forbidden}`);
  assert.equal(probe.aiOpen, true); assert.equal(probe.aiVisible, true);
  assert.equal(probe.runtimeRole.resolvedMain, 1); assert.equal(probe.runtimeRole.resolvedAI, 2); assert(probe.runtimeRole.rejected >= 3);
  assert.equal(probe.runtimeRole.aiWindowsCreated, 2); assert.equal(probe.runtimeRole.aiWindowsClosed, 1);
  const screenshots = [await captureWindow(launch.child.pid, "main", path.join(evidenceDir, "g06-packaged-main.png")), await captureWindow(launch.child.pid, "ai", path.join(evidenceDir, "g06-packaged-ai.png"))];
  const tree = processTree(launch.child.pid);
  // P9-G06 AC4: a multi-window packaged session must not spawn duplicate app
  // processes (no second full IDE bootstrap behind the scenes).
  const appProcesses = (Array.isArray(tree) ? tree : []).filter((entry) => String(entry.Name).toLowerCase() === "koyori-ide.exe");
  assert.equal(appProcesses.length, 1, `expected exactly one koyori-ide.exe process, got ${appProcesses.length}: ${JSON.stringify(tree)}`);
  await launch.flushLog();
  const evidence = { schemaVersion: 1, goal: "P9-G06", status: "passed", evidenceLevel: "P", startedAt, completedAt: new Date().toISOString(), platform: `${process.platform}/${process.arch}`, osRelease: os.release(), nodeVersion: process.version, wailsVersion: cli.version, wailsCLI: { source: cli.source, sha256: cli.sha256, installLog: cli.installLog ?? null }, gitCommit: gitCommit(), gitMetadataAvailable: gitCommit() !== null, sourceFingerprintSha256: source.sha256, sourceFingerprintFiles: source.files, artifact: path.relative(root, artifact).replaceAll("\\", "/"), artifactSha256, artifactBytes: artifactInfo.size, buildTags: ["desktop", "production", "e2e"], buildLog: { file: path.basename(buildLog), sha256: await sha256File(buildLog) }, productionHookAbsence: production, runtimeRoleProbe: probe, processTree: tree, screenshots, applicationLog: { file: "g06-packaged-launch.log", sha256: await sha256File(path.join(evidenceDir, "g06-packaged-launch.log")) }, limitations: gitCommit() === null ? ["The workspace has an empty .git directory; no commit or CI history is claimed."] : [] };
  await writeFile(evidencePath, `${JSON.stringify(evidence, null, 2)}\n`, "utf8"); succeeded = true; console.log(`[g06-packaged-e2e] pass: ${path.relative(root, evidencePath)}`);
} catch (error) {
  const detail = error instanceof Error ? error.stack ?? error.message : String(error);
  await writeFile(failurePath, `${JSON.stringify({ schemaVersion: 1, goal: "P9-G06", status: "failed", evidenceLevel: "U", startedAt, failedAt: new Date().toISOString(), error: detail, retainedWorkDirectory: workDirectory }, null, 2)}\n`, "utf8"); console.error(`[g06-packaged-e2e] FAIL ${detail}`); process.exitCode = 1;
} finally { await stopProcess(launch); if (succeeded) await rm(workDirectory, { recursive: true, force: true }); else console.error(`[g06-packaged-e2e] retained workspace=${workDirectory}`); }
