#!/usr/bin/env node

import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import {
  appendFile,
  mkdir,
  mkdtemp,
  readFile,
  readdir,
  rm,
  stat,
  writeFile,
} from "node:fs/promises";
import http from "node:http";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { PackagedE2EClient } from "./packaged-e2e-driver.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const evidenceDirectory = path.join(root, "build", "e2e-evidence", "p9-g02");
const requestedMode =
  process.argv.find((argument) => argument.startsWith("--mode="))?.slice(7) ??
  "all";
const validModes = new Set(["all", "dev", "packaged"]);
const handshakeTimeoutMilliseconds = 600_000;
const rendererTimeoutMilliseconds = 75_000;
const processStopTimeoutMilliseconds = 8_000;
const publicURL =
  process.env.KOYORI_IDE_HTTP_CLIENT_PUBLIC_URL?.trim() ||
  "https://example.com/";

if (!validModes.has(requestedMode)) {
  throw new Error(
    `invalid mode ${requestedMode}; expected all, dev, or packaged`,
  );
}

function log(stage, detail) {
  console.log(`[http-client-runtime-e2e] ${stage}: ${detail}`);
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
  for (const entry of await readdir(directory, { withFileTypes: true })) {
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
    "build/windows/Taskfile.yml",
    "frontend/package.json",
    "frontend/package-lock.json",
    "frontend/src/main.ts",
    "frontend/src/stores/httpClient.ts",
    "frontend/src/e2e/httpClientProbe.ts",
    "frontend/bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/httpclientservice.ts",
    "frontend/bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/models.ts",
    "scripts/wails-bindings.manifest.json",
    "scripts/check-http-client-production.mjs",
    "scripts/http-client-runtime-e2e.mjs",
  ].map((relativePath) => path.join(root, relativePath));
  const discovered = [
    ...(await listFiles(
      path.join(root, "services"),
      (filePath) =>
        path.basename(filePath).startsWith("http_client") &&
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
  });
  return result.status === 0 ? result.stdout.trim() : null;
}

async function delay(milliseconds) {
  await new Promise((resolve) => setTimeout(resolve, milliseconds));
}

async function waitForJSON(filePath, child, expectedPID = null) {
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
      const value = JSON.parse(await readFile(filePath, "utf8"));
      if (expectedPID !== null)
        assert.equal(
          value.pid,
          expectedPID,
          "handshake PID does not match packaged process",
        );
      return value;
    } catch (error) {
      if (error?.code !== "ENOENT" && !(error instanceof SyntaxError))
        throw error;
    }
    await delay(100);
  }
  throw new Error(`timed out waiting for handshake ${filePath}`);
}

function spawnCaptured(command, args, options) {
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

async function stopProcessTree(child) {
  if (!child?.pid || child.exitCode !== null || child.signalCode !== null)
    return;
  if (process.platform === "win32") {
    spawnSync("taskkill", ["/PID", String(child.pid), "/T", "/F"], {
      encoding: "utf8",
      windowsHide: true,
    });
  } else {
    try {
      process.kill(-child.pid, "SIGTERM");
    } catch (error) {
      if (error?.code !== "ESRCH") throw error;
    }
  }
  await waitForExit(child, processStopTimeoutMilliseconds);
  if (
    child.exitCode === null &&
    child.signalCode === null &&
    process.platform !== "win32"
  ) {
    try {
      process.kill(-child.pid, "SIGKILL");
    } catch (error) {
      if (error?.code !== "ESRCH") throw error;
    }
  }
}

async function stopPIDTree(pid) {
  if (!Number.isSafeInteger(pid) || pid <= 0) return;
  if (process.platform === "win32") {
    spawnSync("taskkill", ["/PID", String(pid), "/T", "/F"], {
      encoding: "utf8",
      windowsHide: true,
    });
  } else {
    try {
      process.kill(pid, "SIGTERM");
    } catch (error) {
      if (error?.code !== "ESRCH") throw error;
    }
  }
  await delay(500);
}

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

async function runCommand(command, args, environment, logPath) {
  const launch = spawnCaptured(command, args, { env: environment });
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

async function availablePort() {
  const server = http.createServer();
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  const address = server.address();
  await new Promise((resolve) => server.close(resolve));
  return address.port;
}

async function readBody(request, limit = 1024 * 1024) {
  const chunks = [];
  let total = 0;
  for await (const chunk of request) {
    total += chunk.length;
    if (total > limit)
      throw new Error("HTTP probe request body exceeded limit");
    chunks.push(chunk);
  }
  return Buffer.concat(chunks).toString("utf8");
}

async function startHTTPFixtures(mode, rawLogPath) {
  await rm(rawLogPath, { force: true });
  const records = [];
  let secondaryOrigin = "";
  const record = async (entry) => {
    const value = {
      sequence: records.length + 1,
      at: new Date().toISOString(),
      mode,
      ...entry,
    };
    records.push(value);
    await appendFile(rawLogPath, `${JSON.stringify(value)}\n`, "utf8");
  };

  const secondary = http.createServer(async (request, response) => {
    const body = await readBody(request);
    await record({
      server: "secondary",
      event: "request",
      method: request.method,
      url: request.url,
      headers: request.headers,
      body,
    });
    response.writeHead(204);
    response.end();
  });
  await new Promise((resolve, reject) => {
    secondary.once("error", reject);
    secondary.listen(0, "127.0.0.1", resolve);
  });
  secondaryOrigin = `http://127.0.0.1:${secondary.address().port}`;

  const primary = http.createServer(async (request, response) => {
    const body = await readBody(request);
    await record({
      server: "primary",
      event: "request",
      method: request.method,
      url: request.url,
      headers: request.headers,
      body,
    });
    const parsed = new URL(request.url, "http://fixture.invalid");
    if (parsed.pathname === "/response") {
      response.writeHead(201, {
        "Content-Type": "application/json",
        "Set-Cookie": "session=must-not-cross-binding",
        "X-Probe-Response": "real-loopback",
      });
      response.end('{"ok":true}');
      return;
    }
    if (parsed.pathname === "/redirect-same") {
      response.writeHead(302, { Location: "/redirect-ok" });
      response.end();
      return;
    }
    if (parsed.pathname === "/redirect-ok") {
      response.writeHead(202, { "Content-Type": "text/plain" });
      response.end("same-origin redirect accepted");
      return;
    }
    if (parsed.pathname === "/redirect-cross") {
      const requestedTarget = parsed.searchParams.get("target");
      if (requestedTarget !== secondaryOrigin) {
        response.writeHead(400);
        response.end("invalid redirect target");
        return;
      }
      response.writeHead(302, { Location: `${secondaryOrigin}/target` });
      response.end();
      return;
    }
    if (parsed.pathname === "/slow") {
      request.once("aborted", () => {
        void record({ server: "primary", event: "aborted", url: request.url });
      });
      response.once("close", () => {
        void record({
          server: "primary",
          event: "closed",
          url: request.url,
          finished: response.writableFinished,
        });
      });
      setTimeout(() => {
        if (response.destroyed || response.writableEnded) return;
        response.writeHead(200, { "Content-Type": "text/plain" });
        response.end("slow response");
      }, 5_000);
      return;
    }
    response.writeHead(404);
    response.end("not found");
  });
  await new Promise((resolve, reject) => {
    primary.once("error", reject);
    primary.listen(0, "127.0.0.1", resolve);
  });
  const primaryOrigin = `http://127.0.0.1:${primary.address().port}`;

  return {
    primaryOrigin,
    secondaryOrigin,
    records,
    close: async () => {
      await Promise.all([
        new Promise((resolve) => primary.close(resolve)),
        new Promise((resolve) => secondary.close(resolve)),
      ]);
    },
  };
}

function validateHTTPLogs(records) {
  const requests = records.filter((entry) => entry.event === "request");
  const primary = requests.filter((entry) => entry.server === "primary");
  const secondary = requests.filter((entry) => entry.server === "secondary");
  const pathCount = (prefix) =>
    primary.filter((entry) => entry.url.startsWith(prefix)).length;
  assert.equal(
    pathCount("/guard/"),
    0,
    "a fail-closed guard request reached the loopback server",
  );
  assert.equal(
    pathCount("/response"),
    1,
    "approved response endpoint call count",
  );
  assert.equal(
    pathCount("/redirect-same"),
    1,
    "same-origin redirect source call count",
  );
  assert.equal(
    pathCount("/redirect-ok"),
    1,
    "same-origin redirect target call count",
  );
  assert.equal(
    pathCount("/redirect-cross"),
    1,
    "cross-origin redirect source call count",
  );
  assert.equal(
    pathCount("/slow?case=cancel"),
    1,
    "cancellation endpoint call count",
  );
  assert.equal(
    pathCount("/slow?case=timeout"),
    1,
    "timeout endpoint call count",
  );
  assert.equal(
    secondary.length,
    0,
    "cross-origin private redirect reached the target server",
  );
  const responseRequest = primary.find((entry) => entry.url === "/response");
  assert.equal(responseRequest.method, "POST");
  assert.equal(responseRequest.headers["x-probe"], "real-packaged-webview");
  assert.equal(responseRequest.body.trim(), "payload");
  return {
    requestCount: requests.length,
    primaryRequestCount: primary.length,
    secondaryRequestCount: secondary.length,
    paths: Object.fromEntries(
      [
        "/response",
        "/redirect-same",
        "/redirect-ok",
        "/redirect-cross",
        "/slow?case=cancel",
        "/slow?case=timeout",
      ].map((value) => [value, pathCount(value)]),
    ),
  };
}

async function runRendererCommand(handshake, token, fixtures) {
  const client = new PackagedE2EClient({ url: handshake.url, token });
  let timeoutID;
  try {
    const timeout = new Promise((_, reject) => {
      timeoutID = setTimeout(
        () =>
          reject(
            new Error("timed out waiting for renderer HTTP-client command"),
          ),
        rendererTimeoutMilliseconds,
      );
    });
    return await Promise.race([
      client.command("http-client-renderer-probe", {
        primaryOrigin: fixtures.primaryOrigin,
        secondaryOrigin: fixtures.secondaryOrigin,
        publicUrl: publicURL,
      }),
      timeout,
    ]);
  } finally {
    clearTimeout(timeoutID);
  }
}

async function preparePinnedCLI(workDirectory, pin) {
  const cliDirectory = path.join(workDirectory, "wails-cli");
  await mkdir(cliDirectory, { recursive: true });
  const executable = path.join(
    cliDirectory,
    process.platform === "win32" ? "wails3.exe" : "wails3",
  );
  const buildLog = path.join(evidenceDirectory, "wails-cli-build.log");
  await runCommand(
    "go",
    ["install", `github.com/wailsapp/wails/v3/cmd/wails3@${pin}`],
    hostEnvironment({ GOBIN: cliDirectory }),
    buildLog,
  );
  const version = spawnSync(executable, ["version"], {
    encoding: "utf8",
    windowsHide: true,
  });
  if (
    version.status !== 0 ||
    !`${version.stdout}${version.stderr}`.includes(pin)
  ) {
    throw new Error(`temporary Wails CLI did not report pinned version ${pin}`);
  }
  return {
    directory: cliDirectory,
    executable,
    version: pin,
    sha256: await sha256File(executable),
  };
}

function executionEnvironment(
  workDirectory,
  cliDirectory,
  handshakePath,
  token,
  runId,
  configDirectory,
) {
  return hostEnvironment({
    PATH: `${cliDirectory}${path.delimiter}${process.env.PATH ?? ""}`,
    VITE_KOYORI_IDE_E2E_HTTP_CLIENT: "1",
    KOYORI_IDE_E2E: "1",
    KOYORI_IDE_E2E_TOKEN: token,
    KOYORI_IDE_E2E_HANDSHAKE: handshakePath,
    KOYORI_IDE_E2E_RUN_ID: runId,
    APPDATA: configDirectory,
    LOCALAPPDATA: configDirectory,
    XDG_CONFIG_HOME: configDirectory,
    KOYORI_IDE_HTTP_CLIENT_E2E_WORKDIR: workDirectory,
  });
}

async function runMode(mode, context) {
  const passPath = path.join(evidenceDirectory, `${mode}-runtime.json`);
  const failurePath = path.join(
    evidenceDirectory,
    `${mode}-runtime.failure.json`,
  );
  const applicationLogPath = path.join(
    evidenceDirectory,
    `${mode}-application.log`,
  );
  const buildLogPath = path.join(evidenceDirectory, `${mode}-build.log`);
  const httpLogPath = path.join(evidenceDirectory, `${mode}-http-server.jsonl`);
  await Promise.all([
    rm(passPath, { force: true }),
    rm(failurePath, { force: true }),
  ]);
  const startedAt = new Date().toISOString();
  const modeDirectory = path.join(context.workDirectory, mode);
  const configDirectory = path.join(modeDirectory, "config");
  const handshakePath = path.join(modeDirectory, "handshake.json");
  await mkdir(configDirectory, { recursive: true });
  const token = randomBytes(32).toString("hex");
  const runId = randomBytes(32).toString("hex");
  const environment = executionEnvironment(
    modeDirectory,
    context.cli.directory,
    handshakePath,
    token,
    runId,
    configDirectory,
  );
  let launch;
  let fixtures;
  let runtimePID = null;
  try {
    fixtures = await startHTTPFixtures(mode, httpLogPath);
    let artifact;
    if (mode === "packaged") {
      log(
        mode,
        "building production artifact with the e2e-only renderer bridge",
      );
      await runCommand(
        context.cli.executable,
        ["build", "-tags", "e2e"],
        environment,
        buildLogPath,
      );
      artifact = path.join(
        root,
        "bin",
        process.platform === "win32" ? "koyori-ide.exe" : "koyori-ide",
      );
      const info = await stat(artifact);
      assert(
        info.size > 1024 * 1024,
        "packaged artifact is unexpectedly small",
      );
      launch = spawnCaptured(artifact, [], { env: environment });
    } else {
      const vitePort = await availablePort();
      environment.EXTRA_TAGS = "e2e";
      environment.WAILS_VITE_PORT = String(vitePort);
      log(mode, `starting real Wails dev mode on Vite port ${vitePort}`);
      launch = spawnCaptured(
        context.cli.executable,
        [
          "dev",
          "-config",
          "./build/config.yml",
          "-port",
          String(vitePort),
          "-nocolour",
        ],
        { env: environment, detached: process.platform !== "win32" },
      );
    }

    const handshake = await waitForJSON(
      handshakePath,
      launch.child,
      mode === "packaged" ? launch.child.pid : null,
    );
    runtimePID = handshake.pid;
    const rendererResult = await runRendererCommand(handshake, token, fixtures);
    assert.equal(
      rendererResult.ok,
      true,
      rendererResult.error ?? "renderer probe reported failure",
    );
    const serverEvidence = validateHTTPLogs(fixtures.records);
    artifact = path.join(
      root,
      "bin",
      process.platform === "win32" ? "koyori-ide.exe" : "koyori-ide",
    );
    const evidence = {
      schemaVersion: 1,
      goal: "P9-G02",
      status: "passed",
      evidenceLevel: mode === "packaged" ? "P" : "I",
      mode,
      startedAt,
      completedAt: new Date().toISOString(),
      platform: `${process.platform}/${process.arch}`,
      wailsVersion: context.cli.version,
      wailsCLISha256: context.cli.sha256,
      gitCommit: context.gitCommit,
      gitMetadataAvailable: context.gitCommit !== null,
      sourceFingerprintSha256: context.source.sha256,
      sourceFingerprintFiles: context.source.files,
      artifact: path.relative(root, artifact).replaceAll("\\", "/"),
      artifactSha256: await sha256File(artifact),
      renderer: rendererResult,
      localHTTPServer: {
        primaryOrigin: fixtures.primaryOrigin,
        secondaryOrigin: fixtures.secondaryOrigin,
        rawLog: path.basename(httpLogPath),
        ...serverEvidence,
      },
      publicURL,
      applicationLog: path.basename(applicationLogPath),
      buildLog: mode === "packaged" ? path.basename(buildLogPath) : null,
      limitations:
        context.gitCommit === null
          ? [
              "The supplied workspace has an empty .git directory, so HEAD is unavailable; the evidence records a deterministic source-input fingerprint instead of claiming a commit.",
            ]
          : [],
    };
    await writeFile(passPath, `${JSON.stringify(evidence, null, 2)}\n`, "utf8");
    await rm(failurePath, { force: true });
    log(
      mode,
      `PASS evidence=${path.relative(root, passPath)} artifact=${evidence.artifactSha256}`,
    );
    return evidence;
  } catch (error) {
    const detail =
      error instanceof Error ? (error.stack ?? error.message) : String(error);
    await writeFile(
      failurePath,
      `${JSON.stringify(
        {
          schemaVersion: 1,
          goal: "P9-G02",
          status: "failed",
          evidenceLevel: "U",
          mode,
          startedAt,
          failedAt: new Date().toISOString(),
          error: detail,
          applicationLog: path.basename(applicationLogPath),
          localHTTPServerLog: path.basename(httpLogPath),
        },
        null,
        2,
      )}\n`,
      "utf8",
    );
    await rm(passPath, { force: true });
    throw error;
  } finally {
    if (runtimePID !== null && runtimePID !== launch?.child.pid)
      await stopPIDTree(runtimePID);
    await stopProcessTree(launch?.child);
    if (launch) await writeFile(applicationLogPath, launch.output(), "utf8");
    await fixtures?.close();
  }
}

await mkdir(evidenceDirectory, { recursive: true });
const workDirectory = await mkdtemp(
  path.join(os.tmpdir(), "koyori-ide-http-client-e2e-"),
);
let succeeded = false;
try {
  const pin = await wailsPin();
  const cli = await preparePinnedCLI(workDirectory, pin);
  const source = await sourceFingerprint();
  const context = { workDirectory, cli, source, gitCommit: gitCommit() };
  log(
    "source",
    `fingerprint=${source.sha256} git=${context.gitCommit ?? "unavailable"}`,
  );
  if (requestedMode === "all" || requestedMode === "dev")
    await runMode("dev", context);
  if (requestedMode === "all" || requestedMode === "packaged")
    await runMode("packaged", context);
  succeeded = true;
} finally {
  if (succeeded) {
    const resolved = path.resolve(workDirectory);
    if (
      path.dirname(resolved) !== path.resolve(os.tmpdir()) ||
      !path.basename(resolved).startsWith("koyori-ide-http-client-e2e-")
    ) {
      throw new Error(
        `refusing to remove unexpected E2E directory ${resolved}`,
      );
    }
    try {
      await removeTemporaryDirectory(resolved);
    } catch (error) {
      const modes =
        requestedMode === "all" ? ["dev", "packaged"] : [requestedMode];
      for (const mode of modes) {
        await rm(path.join(evidenceDirectory, `${mode}-runtime.json`), {
          force: true,
        });
        await writeFile(
          path.join(evidenceDirectory, `${mode}-runtime.failure.json`),
          `${JSON.stringify(
            {
              schemaVersion: 1,
              goal: "P9-G02",
              status: "failed",
              evidenceLevel: "U",
              mode,
              failedAt: new Date().toISOString(),
              error: `runtime verification passed but cleanup failed: ${error instanceof Error ? error.message : String(error)}`,
            },
            null,
            2,
          )}\n`,
          "utf8",
        );
      }
      throw error;
    }
  } else {
    console.error(
      `[http-client-runtime-e2e] retained failure workspace: ${workDirectory}`,
    );
  }
}
