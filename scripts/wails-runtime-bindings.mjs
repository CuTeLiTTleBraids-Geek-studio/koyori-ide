#!/usr/bin/env node

import { spawn, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  mkdir,
  mkdtemp,
  readFile,
  rm,
  writeFile,
} from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

import {
  bindingsDirectory,
  manifestPath,
  readWailsPin,
  root,
} from "./lib/wails-bindings.mjs";

let timeoutMilliseconds = 20_000;
const evidenceDirectory = process.env.KOYORI_IDE_WAILS_BINDINGS_EVIDENCE_DIR?.trim()
  ? path.resolve(process.env.KOYORI_IDE_WAILS_BINDINGS_EVIDENCE_DIR)
  : path.join(root, "build", "e2e-evidence", "p9-g01");
const evidencePath = path.join(evidenceDirectory, "runtime-bindings.json");
const failureEvidencePath = path.join(
  evidenceDirectory,
  "runtime-bindings.failure.json",
);
const logPath = path.join(evidenceDirectory, "runtime-bindings.log");

function readTimeoutMilliseconds() {
  const raw = process.env.KOYORI_IDE_WAILS_RUNTIME_TIMEOUT_MS?.trim() ?? "20000";
  if (!/^\d+$/.test(raw)) {
    throw new Error(`invalid KOYORI_IDE_WAILS_RUNTIME_TIMEOUT_MS: ${raw}`);
  }
  const value = Number(raw);
  if (!Number.isSafeInteger(value) || value < 1_000 || value > 300_000) {
    throw new Error(
      `KOYORI_IDE_WAILS_RUNTIME_TIMEOUT_MS must be between 1000 and 300000: ${raw}`,
    );
  }
  return value;
}

function hostBuildEnvironment() {
  const environment = { ...process.env };
  for (const name of ["GOOS", "GOARCH", "GOFLAGS", "CGO_ENABLED"]) {
    delete environment[name];
  }
  return environment;
}

function run(command, args, cwd = root, env = process.env) {
  const result = spawnSync(command, args, {
    cwd,
    encoding: "utf8",
    env,
    maxBuffer: 16 * 1024 * 1024,
  });
  if (result.error) throw new Error(`${command}: ${result.error.message}`);
  if (result.status !== 0) {
    throw new Error(
      `${command} ${args.join(" ")} exited ${result.status}\n${result.stdout ?? ""}${result.stderr ?? ""}`,
    );
  }
  return `${result.stdout ?? ""}${result.stderr ?? ""}`;
}

function sha256(content) {
  return createHash("sha256").update(content).digest("hex");
}

function delay(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

async function waitForResult(resultPath, child) {
  let spawnError;
  const recordSpawnError = (error) => { spawnError = error; };
  child.once("error", recordSpawnError);
  const deadline = Date.now() + timeoutMilliseconds;
  try {
    while (Date.now() < deadline) {
      if (spawnError) {
        throw new Error(`cannot start Wails runtime: ${spawnError.message}`);
      }
      if (child.exitCode !== null || child.signalCode !== null) {
        throw new Error(
          `Wails runtime exited before renderer result (code=${child.exitCode}, signal=${child.signalCode})`,
        );
      }
      try {
        const source = await readFile(resultPath, "utf8");
        if (source) return JSON.parse(source);
      } catch {
        // The hidden WebView may still be loading the runtime and probe module.
      }
      await delay(100);
    }
    throw new Error(`timed out waiting for renderer result ${resultPath}`);
  } finally {
    child.off("error", recordSpawnError);
  }
}

async function stop(child) {
  if (!child?.pid || child.exitCode !== null || child.signalCode !== null) return;
  child.kill("SIGTERM");
  await Promise.race([
    new Promise((resolve) => child.once("exit", resolve)),
    delay(5_000),
  ]);
  if (child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
}

let workDirectory;
let child;
let output = "";
let succeeded = false;
let failureError = "";
let startedAt = "";
try {
  await mkdir(evidenceDirectory, { recursive: true });
  await rm(evidencePath, { force: true });
  await rm(failureEvidencePath, { force: true });
  startedAt = new Date().toISOString();
  timeoutMilliseconds = readTimeoutMilliseconds();
  const hostEnvironment = hostBuildEnvironment();
  run(
    process.execPath,
    [path.join(root, "scripts", "generate-bindings.mjs")],
    root,
    hostEnvironment,
  );

  workDirectory = await mkdtemp(path.join(os.tmpdir(), "koyori-wails-runtime-"));
  const workspace = path.join(workDirectory, "workspace");
  const attemptedWorkspace = path.join(workDirectory, "attempted-workspace");
  await mkdir(workspace);
  await mkdir(attemptedWorkspace);
  const fixturePath = path.join(workspace, "binding-probe.txt");
  const resultPath = path.join(workspace, "probe-result.json");
  const baseline = "binding runtime baseline\n";
  const replacement = "binding runtime committed\n";
  await writeFile(fixturePath, baseline, "utf8");
  await writeFile(resultPath, "", "utf8");

  const executable = path.join(
    workDirectory,
    process.platform === "win32" ? "binding-runtime-probe.exe" : "binding-runtime-probe",
  );
  run(
    "go",
    [
      "build",
      "-buildvcs=false",
      "-o",
      executable,
      "./internal/bindingruntimeprobe",
    ],
    root,
    hostEnvironment,
  );
  const executableBytes = await readFile(executable);
  const assets = path.join(workDirectory, "assets");
  await mkdir(assets);
  const typescriptModule = await import(pathToFileURL(
    path.join(root, "frontend", "node_modules", "typescript", "lib", "typescript.js"),
  ));
  const typescript = typescriptModule.default ?? typescriptModule;
  const generatedBytes = await readFile(
    path.join(bindingsDirectory, "koyori", "services", "fileservice.ts"),
  );
  const generatedSource = generatedBytes.toString("utf8");
  const manifestBytes = await readFile(manifestPath);
  const compiledSource = typescript.transpileModule(generatedSource, {
    compilerOptions: {
      module: typescript.ModuleKind.ESNext,
      target: typescript.ScriptTarget.ES2022,
    },
  }).outputText.replace(
    /from\s+["']@wailsio\/runtime["']/g,
    'from "/wails/runtime.js"',
  );
  const compiledBindingPath = path.join(assets, "fileservice.js");
  await writeFile(compiledBindingPath, compiledSource, "utf8");
  const baselineHash = sha256(Buffer.from(baseline));
  const emptyHash = sha256(Buffer.alloc(0));
  await writeFile(path.join(assets, "probe-config.json"), `${JSON.stringify({
    fixturePath,
    resultPath,
    attemptedWorkspace,
    baseline,
    replacement,
    baselineHash,
    emptyHash,
  })}\n`, "utf8");
  await writeFile(path.join(assets, "index.html"), `<!doctype html>
<html><head><meta charset="utf-8"><title>Binding runtime probe</title></head>
<body><script type="module">
import * as FileService from "./fileservice.js";
import { Call } from "/wails/runtime.js";
const config = await fetch("./probe-config.json").then((response) => response.json());
async function report(result) {
  await FileService.WriteFileIfUnchanged(
    config.resultPath,
    JSON.stringify(result),
    config.emptyHash,
  );
}
try {
  async function probeHiddenMethod(method, args) {
    let rejected = false;
    let unknownMethod = false;
    let errorMessage = "";
    try {
      await Call.ByName(method, ...args);
    } catch (error) {
      rejected = true;
      errorMessage = error instanceof Error ? error.message : String(error);
      unknownMethod = /unknown bound method name/i.test(errorMessage);
    }
    return { method, rejected, unknownMethod, error: errorMessage };
  }
  const hiddenRootSetter = await probeHiddenMethod(
    "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services.FileService.SetWorkspaceRoot",
    [config.attemptedWorkspace],
  );
  if (!hiddenRootSetter.rejected || !hiddenRootSetter.unknownMethod) {
    throw new Error(
      "hidden root setter was not rejected as an unknown method: " + hiddenRootSetter.error,
    );
  }
  const invalidSecretAccount = "x".repeat(5000);
  const hiddenSecretMethods = await Promise.all([
    ["github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services.SettingsService.GetSecret", [invalidSecretAccount]],
    ["github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services.SettingsService.StoreSecret", [invalidSecretAccount, "must-not-write"]],
    ["github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services.SettingsService.DeleteSecret", [invalidSecretAccount]],
    ["github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services.SettingsService.GetExtensionSecret", [invalidSecretAccount]],
    ["github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services.SettingsService.StoreExtensionSecret", [invalidSecretAccount, "must-not-write"]],
    ["github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services.SettingsService.DeleteExtensionSecret", [invalidSecretAccount]],
  ].map(([method, args]) => probeHiddenMethod(method, args)));
  for (const result of hiddenSecretMethods) {
    if (!result.rejected || !result.unknownMethod) {
      throw new Error(
        "raw secret method was not rejected as an unknown method: "
          + result.method + ": " + result.error,
      );
    }
  }
  const read = await FileService.ReadFile(config.fixturePath);
  if (read !== config.baseline) throw new Error("ReadFile returned unexpected content");
  await FileService.WriteFileIfUnchanged(
    config.fixturePath,
    config.replacement,
    config.baselineHash,
  );
  let conflictRejected = false;
  let conflictError = "";
  try {
    await FileService.WriteFileIfUnchanged(
      config.fixturePath,
      "must not commit\\n",
      config.baselineHash,
    );
  } catch (error) {
    conflictRejected = true;
    conflictError = error instanceof Error ? error.message : String(error);
  }
  const conflictWasBaselineMismatch = /file changed on disk since it was opened/i.test(
    conflictError,
  );
  const committed = await FileService.ReadFile(config.fixturePath);
  await report({
    ok: read === config.baseline
      && committed === config.replacement
      && conflictRejected
      && conflictWasBaselineMismatch,
    readPassed: read === config.baseline,
    controlledWritePassed: committed === config.replacement,
    conflictRejected,
    conflictWasBaselineMismatch,
    conflictError,
    hiddenRootSetterFQN: hiddenRootSetter.method,
    hiddenRootSetterRejected: hiddenRootSetter.rejected,
    hiddenRootSetterUnknown: hiddenRootSetter.unknownMethod,
    hiddenRootSetterError: hiddenRootSetter.error,
    hiddenSecretMethods,
    workspaceRootPreserved: read === config.baseline,
  });
} catch (error) {
  await report({ ok: false, error: error instanceof Error ? error.message : String(error) });
}
</script></body></html>`, "utf8");

  child = spawn(executable, [
    "-workspace",
    workspace,
    "-assets",
    assets,
  ], {
    cwd: root,
    env: hostEnvironment,
    stdio: ["ignore", "pipe", "pipe"],
  });
  child.stdout.on("data", (chunk) => { output += chunk.toString(); });
  child.stderr.on("data", (chunk) => { output += chunk.toString(); });
  const rendererResult = await waitForResult(resultPath, child);
  if (!rendererResult.ok) {
    throw new Error(`renderer binding probe failed: ${rendererResult.error ?? JSON.stringify(rendererResult)}`);
  }
  const committed = await readFile(fixturePath, "utf8");
  if (committed !== replacement) throw new Error("controlled write did not reach disk");

  const evidence = {
    schemaVersion: 1,
    status: "passed",
    evidenceLevel: "I",
    startedAt,
    completedAt: new Date().toISOString(),
    timeoutMilliseconds,
    platform: `${process.platform}/${process.arch}`,
    wailsVersion: await readWailsPin(),
    transport: "real Wails desktop WebView HTTP runtime",
    generatedModule: "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/fileservice.ts",
    manifestSha256: sha256(manifestBytes),
    generatedModuleSha256: sha256(generatedBytes),
    executableSha256: sha256(executableBytes),
    calls: {
      read: { method: "FileService.ReadFile", passed: true },
      controlledWrite: {
        method: "FileService.WriteFileIfUnchanged",
        passed: true,
        baselineSha256: baselineHash,
        committedSha256: sha256(Buffer.from(replacement)),
      },
      staleBaseline: {
        rejected: rendererResult.conflictRejected,
        baselineConflict: rendererResult.conflictWasBaselineMismatch,
        diskPreserved: committed === replacement,
        error: rendererResult.conflictError,
      },
      hiddenRootSetter: {
        method: rendererResult.hiddenRootSetterFQN,
        rejected: rendererResult.hiddenRootSetterRejected,
        unknownMethod: rendererResult.hiddenRootSetterUnknown,
        workspaceRootPreserved: rendererResult.workspaceRootPreserved,
        error: rendererResult.hiddenRootSetterError,
      },
      hiddenSecretMethods: rendererResult.hiddenSecretMethods,
    },
  };
  await writeFile(evidencePath, `${JSON.stringify(evidence, null, 2)}\n`, "utf8");
  succeeded = true;
  console.log(`[wails-runtime-bindings] PASS evidence=${evidencePath}`);
  console.log(`[wails-runtime-bindings] artifact sha256=${evidence.executableSha256}`);
} catch (error) {
  failureError = error instanceof Error ? error.message : String(error);
  output += `\n${error instanceof Error ? error.stack : String(error)}\n`;
  console.error(failureError);
  process.exitCode = 1;
} finally {
  await stop(child);
  await mkdir(evidenceDirectory, { recursive: true });
  if (succeeded && workDirectory) {
    try {
      const resolved = path.resolve(workDirectory);
      if (path.dirname(resolved) !== path.resolve(os.tmpdir())
          || !path.basename(resolved).startsWith("koyori-wails-runtime-")) {
        throw new Error(`refusing to remove unexpected probe directory: ${resolved}`);
      }
      await rm(resolved, { recursive: true, force: true });
    } catch (error) {
      succeeded = false;
      failureError = `runtime probe cleanup failed: ${error instanceof Error ? error.message : String(error)}`;
      output += `\n${failureError}\n`;
      process.exitCode = 1;
      await rm(evidencePath, { force: true });
    }
  }
  if (!succeeded && workDirectory) {
    console.error(`[wails-runtime-bindings] retained failure workspace: ${workDirectory}`);
  }
  await writeFile(logPath, output, "utf8");
  if (succeeded) {
    await rm(failureEvidencePath, { force: true });
  } else {
    await rm(evidencePath, { force: true });
    await writeFile(failureEvidencePath, `${JSON.stringify({
      schemaVersion: 1,
      status: "failed",
      evidenceLevel: "U",
      startedAt: startedAt || null,
      failedAt: new Date().toISOString(),
      platform: `${process.platform}/${process.arch}`,
      error: failureError || "runtime probe failed without an error message",
      log: path.basename(logPath),
    }, null, 2)}\n`, "utf8");
  }
}
