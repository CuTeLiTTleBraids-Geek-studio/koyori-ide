import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { existsSync } from "node:fs";
import {
  mkdtemp,
  readFile,
  readdir,
  rm,
  stat,
} from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

export const root = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "..",
  "..",
);
export const bindingsDirectory = path.join(root, "frontend", "bindings");
export const manifestPath = path.join(root, "scripts", "wails-bindings.manifest.json");
export const obfuscatedBindingsPath = path.join(root, "wails_obfuscated.gen.go");
export const wailsModule = "github.com/wailsapp/wails/v3";
export const canonicalGeneratorFlags = ["-clean=true", "-ts", "-i"];

async function applicationModulePath() {
  const goMod = await readFile(path.join(root, "go.mod"), "utf8");
  const match = goMod.match(/^module\s+(\S+)\s*$/m);
  if (!match) throw new Error("[wails-bindings] go.mod has no module path");
  return match[1];
}

const requiredExports = {
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/aiservice.ts": ["StartStream", "StopStream", "SetConfig", "GetReasoningCapability"],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/windowservice.ts": [
    "Minimise",
    "Maximise",
    "Close",
    "IsMaximised",
    "OpenAIWindow",
    "SendSelectionToAI",
    "OpenPathInExplorer",
  ],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/settingsservice.ts": [
    "LoadSettings",
    "SaveSettings",
    "SavePersonalizationAsset",
    "ReadPersonalizationAsset",
  ],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/agentservice.ts": [
    "ExecCommand",
    "CheckCommand",
    "RequestCommandApproval",
    "ExecuteApprovedCommand",
    "RequestWriteApproval",
    "ExecuteApprovedWrite",
  ],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/taskservice.ts": [
    "LoadTasks",
    "Execute",
    "RequestExecutionApproval",
    "ExecuteApproved",
    "Stop",
  ],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/mcpservice.ts": [
    "ListServers",
    "GetServer",
    "SaveServer",
    "SetServerEnabled",
    "DeleteServer",
    "ConnectServer",
    "DisconnectServer",
    "ListTools",
    "ListAgentMCPTools",
    "ListResources",
    "ReadResource",
    "ListPrompts",
    "GetPrompt",
    "ServerCapabilities",
  ],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/computeruseservice.ts": [
    "RequestOperationApproval",
    "ExecuteApprovedOperation",
  ],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/remoteservice.ts": [
    "RequestCommandApproval",
    "ExecuteCommand",
  ],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/extensionsecurityservice.ts": [
    "RequestExtensionEnableApproval",
    "EnableExtensionWithApproval",
  ],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/fileservice.ts": [
    "ReadFile",
    "WriteFile",
    "WriteFileIfUnchanged",
    "ListDirectory",
  ],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/searchservice.ts": ["ApplyMultiFileReplace"],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/diffservice.ts": ["ApplyDiff", "GetLatestCommitReceipt"],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/httpclientservice.ts": [
    "RequestPrivateNetworkAccess",
    "SendRequest",
  ],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/gitworktreeservice.ts": [
    "ListWorktrees",
    "AddWorktree",
    "RemoveWorktree",
    "PruneWorktrees",
    "LockWorktree",
    "UnlockWorktree",
    "MoveWorktree",
  ],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/gitservice.ts": ["DiscoverRepositories", "GetDiffForSide"],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/gitrebaseservice.ts": [
    "GetRebaseTodoList",
    "GetRebaseStatus",
    "StartInteractiveRebase",
    "ApplyRebaseActions",
    "ContinueRebase",
    "AbortRebase",
    "SkipCommit",
    "IsRebaseInProgress",
  ],
};

const forbiddenExports = {
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/settingsservice.ts": [
    "DeleteExtensionSecret",
    "DeleteSecret",
    "GetAPIKeyForConfig",
    "GetDecryptedAPIKey",
    "GetExtensionSecret",
    "GetSecret",
    "SetConfigPath",
    "StoreExtensionSecret",
    "StoreSecret",
  ],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/aiservice.ts": [
	"SendStream",
	"SendStreamWithContext",
    "SetApp",
    "SetSettingsService",
    "SetPermissionService",
    "SetPresetService",
    "SetProjectRoot",
  ],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/agentservice.ts": [
    "CallMCPTool",
    "Close",
    "SetMCPService",
    "SetSkillsService",
    "SetWorkspaceRoot",
  ],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/aigoalservice.ts": [
    "SetInternalExecutor",
    "SetSnapshotService",
    "SetWorkspaceContext",
  ],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/aipermissionservice.ts": ["SetSettingsService"],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/aiplanservice.ts": [
    "SetInternalExecutor",
    "SetSnapshotService",
    "SetWorkspaceContext",
  ],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/coverageservice.ts": ["SetWorkspaceRoot"],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/crashservice.ts": ["SetDir"],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/eslintservice.ts": ["SetWorkspaceRoot"],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/fileservice.ts": [
    "SetApp",
    "SetWorkspaceRoot",
    "SetWorkspaceRoots",
  ],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/gitservice.ts": ["SetWorkspaceRoot"],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/gitworktreeservice.ts": ["SetWorkspaceContext"],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/gitrebaseservice.ts": ["SetWorkspaceContext"],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/lspservice.ts": [
    "SetFileService",
    "SetWorkspaceRoot",
    "SetWorkspaceRoots",
  ],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/diffservice.ts": [
    "ApplyDiffTransaction",
    "SetFileService",
    "SetSnapshotService",
    "SetWorkspaceContext",
  ],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/skillsservice.ts": ["SetWorkspaceRoot"],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/symbolindexservice.ts": [
    "SetWorkspaceRoot",
    "SetWorkspaceRoots",
  ],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/toolchainservice.ts": ["SetToolPaths", "SetWorkspaceRoot"],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/terminalservice.ts": ["SetApp", "SetWorkspaceRoot"],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/marketplaceservice.ts": [
    "SetApp",
    "SetExtensionLifecycleRequester",
    "SetSecurityService",
    "SetActivationService",
  ],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/mcpservice.ts": [
    "CallTool",
    "Close",
    "ExecuteApprovedTool",
    "RequestToolApproval",
    "SetOnToolsChanged",
  ],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/computeruseservice.ts": [
    "Screenshot",
    "MouseMove",
    "MouseClick",
    "KeyboardType",
    "KeyboardHotkey",
  ],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/extensionsecurityservice.ts": [
    "RemoveInstall",
    "RestoreInstall",
    "SetExtensionEnabled",
  ],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/httpclientservice.ts": ["SetHTTPTransport"],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/layoutservice.ts": ["SetLayoutPath"],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/profileservice.ts": ["SetOnSwitch"],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/projectservice.ts": [
    "SetApp",
    "SetCoverageService",
    "SetEslintService",
    "SetGitService",
    "SetLSPService",
    "SetMCPService",
    "SetMCPWorkspaceRoot",
    "SetSearchService",
    "SetSymbolIndexService",
    "SetToolchainService",
    "SetWorkspaceContext",
  ],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/pullrequestservice.ts": ["SetHTTPTransport"],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/recoveryservice.ts": [
    "SetFileService",
    "SetWorkspaceContext",
  ],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/remoteservice.ts": ["ExecuteCommandContext"],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/searchservice.ts": [
    "ApplyMultiFileReplaceTransaction",
    "SetWorkspaceContext",
    "SetWorkspaceRoot",
    "SetWorkspaceRoots",
  ],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/snapshotservice.ts": ["SetGitService"],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/updateservice.ts": [
    "SetHTTPClient",
    "SetHTTPTransport",
    "SetLookupIP",
  ],
  "github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/windowservice.ts": [
    "AIWindow",
    "AIWindowHandle",
    "CurrentAIWindow",
    "CurrentWindow",
    "SetAIWindow",
    "SetApp",
    "SetWindow",
  ],
};

function normalizeRelative(filePath) {
  return filePath.split(path.sep).join("/");
}

export function parseWailsPin(source) {
  const match = source.match(
    /^\s*github\.com\/wailsapp\/wails\/v3\s+(\S+)/m,
  );
  if (!match) {
    throw new Error(`[wails-bindings] ${wailsModule} is not pinned in go.mod`);
  }
  if (/[xX*]|latest/i.test(match[1])) {
    throw new Error(`[wails-bindings] floating Wails version is forbidden: ${match[1]}`);
  }
  if (!/^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(match[1])) {
    throw new Error(`[wails-bindings] invalid Wails version: ${match[1]}`);
  }
  return match[1];
}

export function checkBindingsOwnership(options = {}) {
  const spawn = options.spawn ?? spawnSync;
  const gitCommand = options.gitCommand ?? "git";
  const result = spawn(
    gitCommand,
    ["ls-files", "--", "frontend/bindings"],
    {
      cwd: options.cwd ?? root,
      encoding: "utf8",
      env: options.env ?? process.env,
      maxBuffer: 16 * 1024 * 1024,
    },
  );
  if (result.error) {
    const detail = result.error.code === "ENOENT"
      ? "executable not found"
      : result.error.message;
    throw new Error(`[bindings] cannot inspect Git ownership: ${detail}`);
  }
  if (result.status !== 0) {
    const detail = `${result.stderr ?? ""}${result.stdout ?? ""}`.trim();
    throw new Error(
      `[bindings] git ls-files failed with exit ${result.status}${detail ? `: ${detail}` : ""}`,
    );
  }
  const tracked = String(result.stdout ?? "")
    .split(/\r?\n/)
    .map((entry) => entry.trim())
    .filter(Boolean);
  if (tracked.length > 0) {
    throw new Error(
      `[bindings] frontend/bindings must remain untracked; Git reports:\n${tracked.join("\n")}`,
    );
  }
  return tracked;
}

export async function readWailsPin() {
  return parseWailsPin(await readFile(path.join(root, "go.mod"), "utf8"));
}

export function parseCLIversion(output) {
  return output.match(/\bv\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?\b/)?.[0] ?? null;
}

export function assertCLIversion(pinnedVersion, output, label = "Wails CLI") {
  const actualVersion = parseCLIversion(output);
  if (!actualVersion) {
    throw new Error(
      `[wails-bindings] ${label} did not report a version; expected ${pinnedVersion}`,
    );
  }
  if (actualVersion !== pinnedVersion) {
    throw new Error(
      `[wails-bindings] ${label} version mismatch: expected ${pinnedVersion}, got ${actualVersion}`,
    );
  }
  return actualVersion;
}

function runProcess(command, args, options = {}) {
  const result = (options.spawn ?? spawnSync)(command, args, {
    cwd: options.cwd ?? root,
    encoding: "utf8",
    env: options.env ?? process.env,
    maxBuffer: 16 * 1024 * 1024,
  });
  if (result.error) {
    const detail = result.error.code === "ENOENT" ? "executable not found" : result.error.message;
    throw new Error(`[wails-bindings] cannot run ${command}: ${detail}`);
  }
  const output = `${result.stdout ?? ""}${result.stderr ?? ""}`;
  if (result.status !== 0) {
    throw new Error(
      `[wails-bindings] ${command} ${args.join(" ")} exited ${result.status}\n${output}`.trim(),
    );
  }
  return output;
}

export function resolvePinnedCLI({
  pinnedVersion,
  env = process.env,
  spawn,
} = {}) {
  if (!pinnedVersion) {
    throw new Error("[wails-bindings] resolvePinnedCLI requires pinnedVersion");
  }
  const explicitCLI = env.WAILS3_BIN?.trim();
  let runner;
  if (explicitCLI) {
    runner = { command: explicitCLI, prefixArgs: [], label: `WAILS3_BIN (${explicitCLI})` };
  } else {
    const cacheDirectory = path.join(
      os.tmpdir(),
      "koyori-wails3",
      pinnedVersion,
      `${process.platform}-${process.arch}`,
    );
    const executable = path.join(
      cacheDirectory,
      process.platform === "win32" ? "wails3.exe" : "wails3",
    );
    if (!existsSync(executable)) {
      const installEnvironment = { ...env, GOBIN: cacheDirectory };
      for (const name of ["GOOS", "GOARCH", "CGO_ENABLED", "GOFLAGS"]) {
        delete installEnvironment[name];
      }
      runProcess(
        env.WAILS3_GO?.trim() || "go",
        ["install", `${wailsModule}/cmd/wails3@${pinnedVersion}`],
        { env: installEnvironment, spawn },
      );
    }
    runner = {
      command: executable,
      prefixArgs: [],
      label: `cached ${wailsModule}/cmd/wails3@${pinnedVersion}`,
    };
  }
  const versionOutput = runProcess(
    runner.command,
    [...runner.prefixArgs, "version"],
    { env, spawn },
  );
  assertCLIversion(pinnedVersion, versionOutput, runner.label);
  return runner;
}

function assertSafeGenerationDirectory(outputDirectory) {
  const resolved = path.resolve(outputDirectory);
  const forbidden = new Set([
    path.parse(resolved).root,
    root,
    os.homedir(),
    os.tmpdir(),
  ].map((entry) => path.resolve(entry)));
  if (forbidden.has(resolved)) {
    throw new Error(`[wails-bindings] refusing unsafe output directory: ${resolved}`);
  }
  return resolved;
}

export function normalizeGeneratorExtraArgs(extraArgs = []) {
  const args = [...extraArgs];
  const obfuscated = args.includes("-obfuscated");
  const outputIndexes = args.flatMap((argument, index) => (
    argument === "-obfuscated-output" ? [index] : []
  ));
  if (outputIndexes.length > 1) {
    throw new Error("[wails-bindings] duplicate -obfuscated-output flags are forbidden");
  }
  if (!obfuscated && outputIndexes.length > 0) {
    throw new Error("[wails-bindings] -obfuscated-output requires -obfuscated");
  }
  if (obfuscated && outputIndexes.length === 0) {
    args.push("-obfuscated-output", root);
  } else if (obfuscated) {
    const value = args[outputIndexes[0] + 1];
    if (!value || path.resolve(value) !== root) {
      throw new Error(
        `[wails-bindings] obfuscated metadata must be generated in the repository root: ${root}`,
      );
    }
  }
  return { args, obfuscated };
}

export async function generateBindings(outputDirectory, options = {}) {
  const pinnedVersion = options.pinnedVersion ?? await readWailsPin();
  const runner = resolvePinnedCLI({
    pinnedVersion,
    env: options.env,
    spawn: options.spawn,
  });
  const output = assertSafeGenerationDirectory(outputDirectory);
  const normalizedExtraArgs = normalizeGeneratorExtraArgs(options.extraArgs);
  if (normalizedExtraArgs.obfuscated) {
    await rm(obfuscatedBindingsPath, { force: true });
  }
  const args = [
    ...runner.prefixArgs,
    "generate",
    "bindings",
    "-d",
    output,
    ...canonicalGeneratorFlags,
    ...normalizedExtraArgs.args,
  ];
  const generatorOutput = runProcess(runner.command, args, {
    env: options.env,
    spawn: options.spawn,
  });
  if (normalizedExtraArgs.obfuscated) {
    let generated;
    try {
      generated = await stat(obfuscatedBindingsPath);
    } catch {
      throw new Error(
        `[wails-bindings] obfuscated metadata was not generated: ${obfuscatedBindingsPath}`,
      );
    }
    if (!generated.isFile() || generated.size === 0) {
      throw new Error(
        `[wails-bindings] obfuscated metadata is empty or invalid: ${obfuscatedBindingsPath}`,
      );
    }
  }
  return {
    pinnedVersion,
    runner,
    output,
    generatorOutput,
    obfuscatedBindingsPath: normalizedExtraArgs.obfuscated ? obfuscatedBindingsPath : null,
  };
}

export async function listFiles(directory) {
  const result = [];
  async function visit(current) {
    const entries = await readdir(current, { withFileTypes: true });
    entries.sort((left, right) => left.name.localeCompare(right.name));
    for (const entry of entries) {
      const full = path.join(current, entry.name);
      if (entry.isDirectory()) {
        await visit(full);
      } else if (entry.isFile()) {
        result.push(normalizeRelative(path.relative(directory, full)));
      }
    }
  }
  if (!existsSync(directory)) return result;
  await visit(directory);
  return result;
}

async function sha256(filePath) {
  const hash = createHash("sha256");
  hash.update(await readFile(filePath));
  return hash.digest("hex");
}

export function parseExports(source) {
  return [...source.matchAll(/^export function\s+([A-Za-z_$][\w$]*)\s*\(/gm)]
    .map((match) => match[1])
    .sort();
}

export async function createBindingsManifest(directory, pinnedVersion) {
  const files = {};
  const exports = {};
  const modulePath = await applicationModulePath();
  const servicePattern = new RegExp(`^${modulePath.replaceAll("/", "\\/")}\\/services\\/[^/]+\\.ts$`);
  for (const relative of await listFiles(directory)) {
    const full = path.join(directory, ...relative.split("/"));
    files[relative] = await sha256(full);
    if (servicePattern.test(relative)
        && !relative.endsWith("/index.ts")
        && !relative.endsWith("/models.ts")) {
      exports[relative] = parseExports(await readFile(full, "utf8"));
    }
  }
  return {
    schemaVersion: 1,
    strategy: "untracked-generate-before-use",
    generator: {
      module: wailsModule,
      version: pinnedVersion,
      flags: canonicalGeneratorFlags,
    },
    files,
    exports,
  };
}

export async function loadBindingsManifest(pinnedVersion) {
  let manifest;
  try {
    manifest = JSON.parse(await readFile(manifestPath, "utf8"));
  } catch (error) {
    throw new Error(`[wails-bindings] cannot read manifest: ${error.message}`);
  }
  const errors = validateManifestMetadata(manifest, pinnedVersion);
  if (errors.length > 0) throw new Error(errors.join("\n"));
  return manifest;
}

export function validateManifestMetadata(manifest, pinnedVersion) {
  const errors = [];
  if (manifest?.schemaVersion !== 1) errors.push("[bindings] unsupported manifest schema");
  if (manifest?.strategy !== "untracked-generate-before-use") {
    errors.push("[bindings] manifest does not declare the untracked generation strategy");
  }
  if (manifest?.generator?.module !== wailsModule) {
    errors.push(`[bindings] manifest generator module must be ${wailsModule}`);
  }
  if (manifest?.generator?.version !== pinnedVersion) {
    errors.push(
      `[bindings] manifest generator version ${manifest?.generator?.version ?? "<missing>"} does not match go.mod ${pinnedVersion}`,
    );
  }
  if (JSON.stringify(manifest?.generator?.flags) !== JSON.stringify(canonicalGeneratorFlags)) {
    errors.push("[bindings] manifest generator flags do not match the canonical flags");
  }
  if (!manifest?.files || Object.keys(manifest.files).length === 0) {
    errors.push("[bindings] manifest contains no generated files");
  }
  if (!manifest?.exports || Object.keys(manifest.exports).length === 0) {
    errors.push("[bindings] manifest contains no service export whitelist");
  }
  return errors;
}

export async function auditBindingsDirectory(directory, manifest) {
  const errors = [];
  let directoryStat;
  try {
    directoryStat = await stat(directory);
  } catch {
    return [`[bindings] missing bindings directory: ${directory}`];
  }
  if (!directoryStat.isDirectory()) {
    return [`[bindings] bindings path is not a directory: ${directory}`];
  }
  const actualFiles = await listFiles(directory);
  if (actualFiles.length === 0) return [`[bindings] bindings directory is empty: ${directory}`];

  const expectedFiles = Object.keys(manifest.files).sort();
  const actualSet = new Set(actualFiles);
  const expectedSet = new Set(expectedFiles);
  for (const relative of expectedFiles) {
    if (!actualSet.has(relative)) {
      errors.push(`[bindings] missing generated file: ${relative}`);
      continue;
    }
    const full = path.join(directory, ...relative.split("/"));
    const actualHash = await sha256(full);
    if (actualHash !== manifest.files[relative]) {
      errors.push(`[bindings] stale or modified generated file: ${relative}`);
    }
  }
  for (const relative of actualFiles) {
    if (!expectedSet.has(relative)) errors.push(`[bindings] unexpected generated file: ${relative}`);
  }

  const modulePath = await applicationModulePath();
  const servicePattern = new RegExp(`^${modulePath.replaceAll("/", "\\/")}\\/services\\/[^/]+\\.ts$`);
  const generatedServiceModules = actualFiles
    .filter((relative) => servicePattern.test(relative)
      && !relative.endsWith("/index.ts")
      && !relative.endsWith("/models.ts"))
    .sort();
  const manifestServiceModules = Object.keys(manifest.exports).sort();
  const generatedServiceSet = new Set(generatedServiceModules);
  const manifestServiceSet = new Set(manifestServiceModules);
  for (const relative of generatedServiceModules) {
    if (!manifestServiceSet.has(relative)) {
      errors.push(`[bindings] generated service module is missing from export whitelist: ${relative}`);
    }
  }
  for (const relative of manifestServiceModules) {
    if (!generatedServiceSet.has(relative)) {
      errors.push(`[bindings] export whitelist contains a phantom service module: ${relative}`);
    }
  }

  for (const [relative, expectedExports] of Object.entries(manifest.exports)) {
    if (!actualSet.has(relative)) continue;
    const full = path.join(directory, ...relative.split("/"));
    const source = await readFile(full, "utf8");
    if (!source.includes("This file is automatically generated. DO NOT EDIT")) {
      errors.push(`[bindings] ${relative} lacks the Wails generated-file marker`);
    }
    if (/\$Call\.ByName\s*\(/.test(source)) {
      errors.push(`[bindings] ${relative} contains a name-based FQN call`);
    }
    const actualExports = parseExports(source);
    if (JSON.stringify(actualExports) !== JSON.stringify(expectedExports)) {
      errors.push(`[bindings] ${relative} export whitelist drifted`);
    }
  }

  for (const [relative, methods] of Object.entries(requiredExports)) {
    const actual = new Set(manifest.exports[relative] ?? []);
    for (const method of methods) {
      if (!actual.has(method)) errors.push(`[bindings] ${relative}: missing required export ${method}`);
    }
  }
  for (const [relative, methods] of Object.entries(forbiddenExports)) {
    const actual = new Set(manifest.exports[relative] ?? []);
    for (const method of methods) {
      if (actual.has(method)) errors.push(`[bindings] ${relative}: forbidden export ${method}`);
    }
  }
  return errors;
}

async function collectFrontendSourceFiles(directory) {
  const files = [];
  async function visit(current) {
    const entries = await readdir(current, { withFileTypes: true });
    for (const entry of entries) {
      const full = path.join(current, entry.name);
      if (entry.isDirectory()) {
        await visit(full);
      } else if (entry.isFile()
        && /\.(?:ts|vue)$/.test(entry.name)
        && !/\.test\.(?:ts|vue)$/.test(entry.name)) {
        files.push(full);
      }
    }
  }
  await visit(directory);
  return files;
}

export async function auditFrontendBindingUsage(sourceRoot = path.join(root, "frontend", "src")) {
  const errors = [];
  const allowedEventsImport = /import\s*\{\s*Events\s*\}\s*from\s*["']@wailsio\/runtime["']\s*;?/g;
  const runtimeModulePattern = /["']@wailsio\/runtime(?:\/[^"']*)?["']/;
  const manualCallPattern = /\b\$?Call\s*(?:\.\s*By(?:ID|Name)\b|\[\s*["']By(?:ID|Name)["']\s*\])/;
  const rootSetterPattern = /\b(?:SetWorkspaceRoots?|SetProjectRoot|setWorkspaceRoots?|setProjectRoot)\s*\(/;
  const rawSecretBindingPattern = /\b(?:GetSecret|StoreSecret|DeleteSecret|GetExtensionSecret|StoreExtensionSecret|DeleteExtensionSecret)\s*\(/;
  for (const full of await collectFrontendSourceFiles(sourceRoot)) {
    const source = await readFile(full, "utf8");
    const relative = normalizeRelative(path.relative(root, full));
    const sourceWithoutAllowedEvents = source.replace(allowedEventsImport, "");
    if (runtimeModulePattern.test(sourceWithoutAllowedEvents)
        || manualCallPattern.test(sourceWithoutAllowedEvents)) {
      errors.push(`[bindings] renderer source bypasses generated bindings: ${relative}`);
    }
    if (rootSetterPattern.test(source)) {
      errors.push(`[bindings] renderer source calls a hidden root setter: ${relative}`);
    }
    if (rawSecretBindingPattern.test(source)) {
      errors.push(`[bindings] renderer source calls a forbidden raw secret binding: ${relative}`);
    }
  }

  const extensionHost = await readFile(
    path.join(sourceRoot, "lib", "extensionHost", "extensionHost.ts"),
    "utf8",
  );
  for (const callsite of [
    "TaskServiceBindings.RequestExecutionApproval(",
    "TaskServiceBindings.ExecuteApproved(",
    "TaskServiceBindings.Stop(",
  ]) {
    if (!extensionHost.includes(callsite)) {
      errors.push(`[bindings] extensionHost.ts is missing generated callsite ${callsite}`);
    }
  }

  const httpClientStore = await readFile(
    path.join(sourceRoot, "stores", "httpClient.ts"),
    "utf8",
  );
  const httpClientStaticImport =
    'import * as HTTPClientServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/httpclientservice.js";';
  if (!httpClientStore.includes(httpClientStaticImport)) {
    errors.push("[bindings] HTTP Client production store must statically import its generated binding");
  }
  if (/@vite-ignore|\bbindingPath\b|\bloadBindings\b|import\s*\(/.test(httpClientStore)) {
    errors.push("[bindings] HTTP Client production store contains a runtime binding loader");
  }
  return errors;
}

export async function compareBindingDirectories(left, right) {
  const errors = [];
  const leftFiles = await listFiles(left);
  const rightFiles = await listFiles(right);
  const all = new Set([...leftFiles, ...rightFiles]);
  for (const relative of [...all].sort()) {
    if (!leftFiles.includes(relative)) {
      errors.push(`[bindings] checked directory is missing ${relative}`);
      continue;
    }
    if (!rightFiles.includes(relative)) {
      errors.push(`[bindings] checked directory has extra ${relative}`);
      continue;
    }
    const leftHash = await sha256(path.join(left, ...relative.split("/")));
    const rightHash = await sha256(path.join(right, ...relative.split("/")));
    if (leftHash !== rightHash) errors.push(`[bindings] generated drift: ${relative}`);
  }
  return errors;
}

export async function withTemporaryBindings(prefix, callback) {
  const directory = await mkdtemp(path.join(os.tmpdir(), prefix));
  try {
    return await callback(directory);
  } finally {
    const resolved = path.resolve(directory);
    if (path.dirname(resolved) !== path.resolve(os.tmpdir())
        || !path.basename(resolved).startsWith(prefix)) {
      throw new Error(`[wails-bindings] refusing to remove unexpected temp path: ${resolved}`);
    }
    await rm(resolved, { recursive: true, force: true });
  }
}

export function formatErrors(errors) {
  return errors.length === 0 ? "" : `${errors.join("\n")}\n`;
}
