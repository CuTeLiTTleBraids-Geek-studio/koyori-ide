/** Versioned, declarative language-pack runtime. It never launches processes. */
import goLanguagePack from "../../../services/languagepacks/go.language-pack.json";
import typescriptLanguagePack from "../../../services/languagepacks/typescript.language-pack.json";

export const LANGUAGE_PACK_SCHEMA_VERSION = "1.0" as const;
export const LANGUAGE_PACK_ENGINE_API_VERSION = "1.0" as const;
export const LANGUAGE_PACK_LOCAL_HOST_PROTOCOL = "language.local.v1" as const;

export interface LanguagePackPlatform {
  os: "windows" | "darwin" | "linux";
  arch: "amd64" | "arm64";
}

export interface LanguagePackCompatibility {
  engineApi: typeof LANGUAGE_PACK_ENGINE_API_VERSION;
  hostProtocol: typeof LANGUAGE_PACK_LOCAL_HOST_PROTOCOL;
  platforms: readonly LanguagePackPlatform[];
}

export interface LanguagePackLanguage {
  id: string;
  extensions: readonly string[];
  filenames: readonly string[];
}

export interface LanguagePackRuntimeContribution {
  id: string;
  version: string;
  manifestSha256: string;
  languages: readonly LanguagePackLanguage[];
}

export interface LanguagePackExecutable {
  commandName: string;
  kind: string;
}

export interface LanguagePackServer {
  id: string;
  statusOrder: number;
  languages: readonly string[];
  aliases: readonly string[];
  executables: readonly LanguagePackExecutable[];
  args: readonly string[];
  installHint: string;
  workspaceNode: boolean;
  initializationProfile: "go" | "typescript" | "generic";
  configurationSections: readonly string[];
  configurationResponse: "full" | "preferences";
  versionExecutable?: string;
  versionArgs: readonly string[];
  versionPin?: string;
  preferReactWorkspace: boolean;
  reactAware: boolean;
}

export interface LanguagePackDebugger {
  id: string;
  protocol: "dap" | "cdp";
  languages: readonly string[];
  executable: string;
  args: readonly string[];
  installHint: string;
}

export interface LanguagePackToolchainCommand {
  id: string;
  label: string;
  language: string;
  executable: string;
  args: readonly string[];
  description: string;
  fileScoped: boolean;
}

export interface LanguagePackToolchainTool {
  name: string;
  installHint: string;
}

export interface LanguagePackToolchain {
  commands: readonly LanguagePackToolchainCommand[];
  tools: readonly LanguagePackToolchainTool[];
}

export interface LanguagePackManifest {
  schemaVersion: typeof LANGUAGE_PACK_SCHEMA_VERSION;
  id: string;
  version: string;
  displayName: string;
  compatibility: LanguagePackCompatibility;
  languages: readonly LanguagePackLanguage[];
  rootMarkers: readonly string[];
  servers: readonly LanguagePackServer[];
  debuggers?: readonly LanguagePackDebugger[];
  toolchain?: LanguagePackToolchain;
  permissions: readonly string[];
  configurationSchema: Readonly<Record<string, unknown>>;
  integrity: Readonly<{ manifestSha256: string }>;
}

const manifestKeys = new Set([
  "schemaVersion",
  "id",
  "version",
  "displayName",
  "compatibility",
  "languages",
  "rootMarkers",
  "servers",
  "debuggers",
  "toolchain",
  "permissions",
  "configurationSchema",
  "integrity",
]);
const compatibilityKeys = new Set(["engineApi", "hostProtocol", "platforms"]);
const platformKeys = new Set(["os", "arch"]);
const languageKeys = new Set(["id", "extensions", "filenames"]);
const runtimeContributionKeys = new Set([
  "id",
  "version",
  "manifestSha256",
  "languages",
]);
const serverKeys = new Set([
  "id",
  "statusOrder",
  "languages",
  "aliases",
  "executables",
  "args",
  "installHint",
  "workspaceNode",
  "initializationProfile",
  "configurationSections",
  "configurationResponse",
  "versionExecutable",
  "versionArgs",
  "versionPin",
  "preferReactWorkspace",
  "reactAware",
]);
const executableKeys = new Set(["commandName", "kind"]);
const debuggerKeys = new Set([
  "id",
  "protocol",
  "languages",
  "executable",
  "args",
  "installHint",
]);
const toolchainKeys = new Set(["commands", "tools"]);
const toolchainCommandKeys = new Set([
  "id",
  "label",
  "language",
  "executable",
  "args",
  "description",
  "fileScoped",
]);
const toolchainToolKeys = new Set(["name", "installHint"]);
const semverPattern =
  /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*))*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$/;
const idPattern = /^[a-z0-9]+(?:[.-][a-z0-9]+)*$/;
const commandPattern = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;
const sha256Pattern = /^[a-f0-9]{64}$/;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function assertExactKeys(
  value: Record<string, unknown>,
  allowed: Set<string>,
  label: string,
): void {
  for (const key of Object.keys(value)) {
    if (!allowed.has(key)) {
      throw new Error(
        `Language pack ${label} contains unsupported field "${key}"`,
      );
    }
  }
}

function requireString(value: unknown, label: string): string {
  if (typeof value !== "string" || value.length === 0) {
    throw new Error(`Language pack ${label} must be a non-empty string`);
  }
  return value;
}

function requireStringArray(value: unknown, label: string): string[] {
  if (
    !Array.isArray(value) ||
    value.some((item) => typeof item !== "string" || item.length === 0)
  ) {
    throw new Error(
      `Language pack ${label} must be an array of non-empty strings`,
    );
  }
  const result = [...(value as string[])];
  if (new Set(result).size !== result.length) {
    throw new Error(`Language pack ${label} must not contain duplicates`);
  }
  return result;
}

function requireBoolean(value: unknown, label: string): boolean {
  if (typeof value !== "boolean")
    throw new Error(`Language pack ${label} must be boolean`);
  return value;
}

function freezeManifest(manifest: {
  schemaVersion: typeof LANGUAGE_PACK_SCHEMA_VERSION;
  id: string;
  version: string;
  displayName: string;
  compatibility: LanguagePackCompatibility;
  languages: LanguagePackLanguage[];
  rootMarkers: string[];
  servers: LanguagePackServer[];
  debuggers?: LanguagePackDebugger[];
  toolchain?: LanguagePackToolchain;
  permissions: string[];
  configurationSchema: Record<string, unknown>;
  integrity: { manifestSha256: string };
}): LanguagePackManifest {
  return Object.freeze({
    ...manifest,
    compatibility: Object.freeze({
      ...manifest.compatibility,
      platforms: Object.freeze(
        manifest.compatibility.platforms.map((platform) =>
          Object.freeze({ ...platform }),
        ),
      ),
    }),
    languages: Object.freeze(
      manifest.languages.map((language) =>
        Object.freeze({
          ...language,
          extensions: Object.freeze([...language.extensions]),
          filenames: Object.freeze([...language.filenames]),
        }),
      ),
    ),
    rootMarkers: Object.freeze([...manifest.rootMarkers]),
    servers: Object.freeze(
      manifest.servers.map((server) =>
        Object.freeze({
          ...server,
          languages: Object.freeze([...server.languages]),
          aliases: Object.freeze([...server.aliases]),
          executables: Object.freeze(
            server.executables.map((executable) =>
              Object.freeze({ ...executable }),
            ),
          ),
          args: Object.freeze([...server.args]),
          configurationSections: Object.freeze([
            ...server.configurationSections,
          ]),
          ...(server.versionExecutable
            ? { versionExecutable: server.versionExecutable }
            : {}),
          versionArgs: Object.freeze([...server.versionArgs]),
          ...(server.versionPin ? { versionPin: server.versionPin } : {}),
        }),
      ),
    ),
    ...(manifest.debuggers
      ? {
          debuggers: Object.freeze(
            manifest.debuggers.map((debuggerSpec) =>
              Object.freeze({
                ...debuggerSpec,
                languages: Object.freeze([...debuggerSpec.languages]),
                args: Object.freeze([...debuggerSpec.args]),
              }),
            ),
          ),
        }
      : {}),
    ...(manifest.toolchain
      ? {
          toolchain: Object.freeze({
            commands: Object.freeze(
              manifest.toolchain.commands.map((command) =>
                Object.freeze({
                  ...command,
                  args: Object.freeze([...command.args]),
                }),
              ),
            ),
            tools: Object.freeze(
              manifest.toolchain.tools.map((tool) =>
                Object.freeze({ ...tool }),
              ),
            ),
          }),
        }
      : {}),
    permissions: Object.freeze([...manifest.permissions]),
    configurationSchema: Object.freeze({ ...manifest.configurationSchema }),
    integrity: Object.freeze({ ...manifest.integrity }),
  });
}

/** Validate the selector-only snapshot returned by the backend service. */
export function validateLanguagePackRuntimeContribution(
  input: unknown,
): LanguagePackRuntimeContribution {
  if (!isRecord(input))
    throw new Error("Language pack runtime contribution must be an object");
  assertExactKeys(input, runtimeContributionKeys, "runtime contribution");
  const id = requireString(input.id, "runtime contribution id");
  const version = requireString(input.version, "runtime contribution version");
  const manifestSha256 = requireString(
    input.manifestSha256,
    "runtime contribution manifestSha256",
  );
  if (!idPattern.test(id))
    throw new Error(`Language pack id is invalid: ${id}`);
  if (!semverPattern.test(version))
    throw new Error(`Language pack version is invalid: ${version}`);
  if (!sha256Pattern.test(manifestSha256)) {
    throw new Error(
      "Language pack runtime contribution manifestSha256 is invalid",
    );
  }
  if (!Array.isArray(input.languages) || input.languages.length === 0) {
    throw new Error(
      "Language pack runtime contribution languages must be a non-empty array",
    );
  }
  const languageIds = new Set<string>();
  const selectors = new Set<string>();
  const languages = input.languages.map((raw, index) => {
    if (!isRecord(raw)) {
      throw new Error(
        `Language pack runtime contribution languages[${index}] must be an object`,
      );
    }
    assertExactKeys(
      raw,
      languageKeys,
      `runtime contribution languages[${index}]`,
    );
    const languageId = requireString(
      raw.id,
      `runtime contribution languages[${index}].id`,
    );
    if (!idPattern.test(languageId) || languageIds.has(languageId)) {
      throw new Error(
        `Language pack runtime contribution language is invalid or duplicated: ${languageId}`,
      );
    }
    languageIds.add(languageId);
    const extensions = requireStringArray(
      raw.extensions,
      `runtime contribution languages[${index}].extensions`,
    );
    const filenames = requireStringArray(
      raw.filenames,
      `runtime contribution languages[${index}].filenames`,
    );
    if (extensions.length === 0 && filenames.length === 0) {
      throw new Error(
        `Language pack runtime contribution language ${languageId} needs a selector`,
      );
    }
    for (const extension of extensions) {
      if (!/^\.[^./\\]+$/.test(extension)) {
        throw new Error(
          `Language pack runtime contribution extension is invalid: ${extension}`,
        );
      }
      const selector = `extension:${extension.toLowerCase()}`;
      if (selectors.has(selector))
        throw new Error(`Ambiguous language extension: ${extension}`);
      selectors.add(selector);
    }
    for (const filename of filenames) {
      if (filename.includes("/") || filename.includes("\\")) {
        throw new Error(
          `Language pack filename must be a basename: ${filename}`,
        );
      }
      const selector = `filename:${filename.toLowerCase()}`;
      if (selectors.has(selector))
        throw new Error(`Ambiguous language filename: ${filename}`);
      selectors.add(selector);
    }
    return Object.freeze({
      id: languageId,
      extensions: Object.freeze(extensions),
      filenames: Object.freeze(filenames),
    });
  });
  return Object.freeze({
    id,
    version,
    manifestSha256,
    languages: Object.freeze(languages),
  });
}

/** Validate and normalize a closed v1 manifest. No server process is started here. */
export function validateLanguagePackManifest(
  input: unknown,
): LanguagePackManifest {
  if (!isRecord(input))
    throw new Error("Language pack manifest must be an object");
  assertExactKeys(input, manifestKeys, "manifest");

  if (input.schemaVersion !== LANGUAGE_PACK_SCHEMA_VERSION) {
    throw new Error(
      `Unsupported language pack schema version: ${String(input.schemaVersion)}`,
    );
  }
  const id = requireString(input.id, "id");
  if (!idPattern.test(id))
    throw new Error(`Language pack id is invalid: ${id}`);
  const version = requireString(input.version, "version");
  if (!semverPattern.test(version))
    throw new Error(`Language pack version is invalid: ${version}`);
  const displayName = requireString(input.displayName, "displayName");

  if (!isRecord(input.compatibility))
    throw new Error("Language pack compatibility must be an object");
  assertExactKeys(input.compatibility, compatibilityKeys, "compatibility");
  if (input.compatibility.engineApi !== LANGUAGE_PACK_ENGINE_API_VERSION) {
    throw new Error(
      `Unsupported language pack engine API: ${String(input.compatibility.engineApi)}`,
    );
  }
  if (input.compatibility.hostProtocol !== LANGUAGE_PACK_LOCAL_HOST_PROTOCOL) {
    throw new Error(
      `Unsupported language pack host protocol: ${String(input.compatibility.hostProtocol)}`,
    );
  }
  if (
    !Array.isArray(input.compatibility.platforms) ||
    input.compatibility.platforms.length === 0 ||
    input.compatibility.platforms.length > 6
  ) {
    throw new Error("Language pack compatibility platforms are invalid");
  }
  const platformIds = new Set<string>();
  const platforms = input.compatibility.platforms.map((platform, index) => {
    if (!isRecord(platform))
      throw new Error(
        `Language pack compatibility platforms[${index}] is invalid`,
      );
    assertExactKeys(
      platform,
      platformKeys,
      `compatibility.platforms[${index}]`,
    );
    if (
      platform.os !== "windows" &&
      platform.os !== "darwin" &&
      platform.os !== "linux"
    ) {
      throw new Error(
        `Unsupported language pack operating system: ${String(platform.os)}`,
      );
    }
    if (platform.arch !== "amd64" && platform.arch !== "arm64") {
      throw new Error(
        `Unsupported language pack architecture: ${String(platform.arch)}`,
      );
    }
    const id = `${platform.os}/${platform.arch}`;
    if (platformIds.has(id))
      throw new Error(`Duplicate language pack platform: ${id}`);
    platformIds.add(id);
    return { os: platform.os, arch: platform.arch } as LanguagePackPlatform;
  });
  const compatibility: LanguagePackCompatibility = {
    engineApi: LANGUAGE_PACK_ENGINE_API_VERSION,
    hostProtocol: LANGUAGE_PACK_LOCAL_HOST_PROTOCOL,
    platforms,
  };

  if (!Array.isArray(input.languages) || input.languages.length === 0) {
    throw new Error("Language pack languages must be a non-empty array");
  }
  const languageIds = new Set<string>();
  const selectors = new Set<string>();
  const languages = input.languages.map((raw, index) => {
    if (!isRecord(raw))
      throw new Error(`Language pack languages[${index}] must be an object`);
    assertExactKeys(raw, languageKeys, `languages[${index}]`);
    const languageId = requireString(raw.id, `languages[${index}].id`);
    if (!idPattern.test(languageId))
      throw new Error(`Language id is invalid: ${languageId}`);
    if (languageIds.has(languageId))
      throw new Error(`Duplicate language id: ${languageId}`);
    languageIds.add(languageId);
    const extensions = requireStringArray(
      raw.extensions,
      `languages[${index}].extensions`,
    );
    if (extensions.some((extension) => !/^\.[^./\\]+$/.test(extension))) {
      throw new Error(
        `Language pack languages[${index}].extensions must be a file extension`,
      );
    }
    const filenames = requireStringArray(
      raw.filenames,
      `languages[${index}].filenames`,
    );
    if (extensions.length === 0 && filenames.length === 0) {
      throw new Error(
        `Language pack languages[${index}] must declare a selector`,
      );
    }
    for (const extension of extensions) {
      const selector = `extension:${extension.toLowerCase()}`;
      if (selectors.has(selector))
        throw new Error(`Ambiguous language extension: ${extension}`);
      selectors.add(selector);
    }
    for (const filename of filenames) {
      if (filename.includes("/") || filename.includes("\\")) {
        throw new Error(
          `Language pack filename must be a basename: ${filename}`,
        );
      }
      const selector = `filename:${filename.toLowerCase()}`;
      if (selectors.has(selector))
        throw new Error(`Ambiguous language filename: ${filename}`);
      selectors.add(selector);
    }
    return { id: languageId, extensions, filenames };
  });

  const rootMarkers = requireStringArray(input.rootMarkers, "rootMarkers");
  let debuggers: LanguagePackDebugger[] | undefined;
  if (input.debuggers !== undefined) {
    if (!Array.isArray(input.debuggers))
      throw new Error("Language pack debuggers must be an array");
    const debuggerIds = new Set<string>();
    const debuggerLanguageOwners = new Map<string, string>();
    debuggers = input.debuggers.map((raw, index) => {
      if (!isRecord(raw))
        throw new Error(`Language pack debuggers[${index}] must be an object`);
      assertExactKeys(raw, debuggerKeys, `debuggers[${index}]`);
      const id = requireString(raw.id, `debuggers[${index}].id`);
      const protocol = requireString(
        raw.protocol,
        `debuggers[${index}].protocol`,
      );
      const languages = requireStringArray(
        raw.languages,
        `debuggers[${index}].languages`,
      );
      const executable = requireString(
        raw.executable,
        `debuggers[${index}].executable`,
      );
      const args = requireStringArray(raw.args, `debuggers[${index}].args`);
      const installHint = requireString(
        raw.installHint,
        `debuggers[${index}].installHint`,
      );
      if (
        !idPattern.test(id) ||
        debuggerIds.has(id) ||
        (protocol !== "dap" && protocol !== "cdp") ||
        !commandPattern.test(executable)
      ) {
        throw new Error(
          `Language pack debugger ${id} is invalid or duplicated`,
        );
      }
      const localLanguages = new Set<string>();
      for (const languageId of languages) {
        if (!languageIds.has(languageId) || localLanguages.has(languageId)) {
          throw new Error(
            `Language pack debugger ${id} references unknown or duplicate language`,
          );
        }
        localLanguages.add(languageId);
        const owner = debuggerLanguageOwners.get(languageId);
        if (owner)
          throw new Error(
            `Language ${languageId} is assigned to both debuggers ${owner} and ${id}`,
          );
        debuggerLanguageOwners.set(languageId, id);
      }
      if (
        args.some((arg) => arg.includes("\0")) ||
        installHint.includes("\0")
      ) {
        throw new Error(`Language pack debugger ${id} contains NUL`);
      }
      debuggerIds.add(id);
      return {
        id,
        protocol,
        languages,
        executable,
        args,
        installHint,
      } as LanguagePackDebugger;
    });
  }
  let toolchain: LanguagePackToolchain | undefined;
  if (input.toolchain !== undefined) {
    if (!isRecord(input.toolchain))
      throw new Error("Language pack toolchain must be an object");
    assertExactKeys(input.toolchain, toolchainKeys, "toolchain");
    if (
      !Array.isArray(input.toolchain.commands) ||
      input.toolchain.commands.length === 0
    ) {
      throw new Error(
        "Language pack toolchain commands must be a non-empty array",
      );
    }
    if (!Array.isArray(input.toolchain.tools)) {
      throw new Error("Language pack toolchain tools must be an array");
    }
    const languageIds = new Set(languages.map((language) => language.id));
    const toolNames = new Set<string>();
    const tools = input.toolchain.tools.map((raw, index) => {
      if (!isRecord(raw))
        throw new Error(
          `Language pack toolchain.tools[${index}] must be an object`,
        );
      assertExactKeys(raw, toolchainToolKeys, `toolchain.tools[${index}]`);
      const name = requireString(raw.name, `toolchain.tools[${index}].name`);
      if (!commandPattern.test(name) || toolNames.has(name)) {
        throw new Error(
          `Language pack toolchain tool is invalid or duplicated: ${name}`,
        );
      }
      toolNames.add(name);
      const installHint = requireString(
        raw.installHint,
        `toolchain.tools[${index}].installHint`,
      );
      if (installHint.includes("\0"))
        throw new Error("Language pack toolchain install hint contains NUL");
      return { name, installHint };
    });
    const commandIds = new Set<string>();
    const commands = input.toolchain.commands.map((raw, index) => {
      if (!isRecord(raw))
        throw new Error(
          `Language pack toolchain.commands[${index}] must be an object`,
        );
      assertExactKeys(
        raw,
        toolchainCommandKeys,
        `toolchain.commands[${index}]`,
      );
      const id = requireString(raw.id, `toolchain.commands[${index}].id`);
      const label = requireString(
        raw.label,
        `toolchain.commands[${index}].label`,
      );
      const language = requireString(
        raw.language,
        `toolchain.commands[${index}].language`,
      );
      const executable = requireString(
        raw.executable,
        `toolchain.commands[${index}].executable`,
      );
      const args = requireStringArray(
        raw.args,
        `toolchain.commands[${index}].args`,
      );
      const description = requireString(
        raw.description,
        `toolchain.commands[${index}].description`,
      );
      if (
        !idPattern.test(id) ||
        commandIds.has(id) ||
        !languageIds.has(language) ||
        !commandPattern.test(executable)
      ) {
        throw new Error(
          `Language pack toolchain command ${id} is invalid, duplicated, or references an unknown language`,
        );
      }
      if (!toolNames.has(executable))
        throw new Error(
          `Language pack toolchain command ${id} uses an undeclared tool`,
        );
      if (typeof raw.fileScoped !== "boolean")
        throw new Error(
          `Language pack toolchain command ${id} fileScoped must be boolean`,
        );
      if ([...args, description].some((value) => value.includes("\0")))
        throw new Error(`Language pack toolchain command ${id} contains NUL`);
      commandIds.add(id);
      return {
        id,
        label,
        language,
        executable,
        args,
        description,
        fileScoped: raw.fileScoped,
      };
    });
    toolchain = { commands, tools };
  }
  const serversRaw = input.servers;
  if (!Array.isArray(serversRaw))
    throw new Error("Language pack servers must be an array");
  const permissions = requireStringArray(input.permissions, "permissions");
  const configurationSchema = input.configurationSchema;
  if (
    !isRecord(configurationSchema) ||
    Object.keys(configurationSchema).length !== 0
  ) {
    throw new Error(
      "Language pack configuration schemas are not supported by this runtime",
    );
  }
  if (
    !isRecord(input.integrity) ||
    typeof input.integrity.manifestSha256 !== "string" ||
    !sha256Pattern.test(input.integrity.manifestSha256)
  ) {
    throw new Error(
      "Language pack integrity.manifestSha256 must be a lowercase SHA-256",
    );
  }

  const requiresProcessLaunch =
    serversRaw.length > 0 ||
    (debuggers?.length ?? 0) > 0 ||
    (toolchain?.commands.length ?? 0) > 0;
  const permissionSet = new Set(permissions);
  if (
    permissions.some(
      (permission) =>
        permission !== "workspace.read" &&
        permission !== "process.launch" &&
        permission !== "tool.execute",
    )
  ) {
    throw new Error("Language pack permissions are not supported");
  }
  if (
    permissionSet.size !== permissions.length ||
    !permissionSet.has("workspace.read")
  ) {
    throw new Error(
      "Language pack permissions must be unique and include workspace.read",
    );
  }
  if (!requiresProcessLaunch) {
    if (permissions.length !== 1 || permissions[0] !== "workspace.read") {
      throw new Error(
        "Serverless language packs must request exactly [workspace.read]",
      );
    }
  } else if (!permissionSet.has("process.launch")) {
    throw new Error(
      "Language pack server execution is not supported without process.launch",
    );
  }

  const servers = serversRaw.map((raw, index) => {
    if (!isRecord(raw))
      throw new Error(
        `Language pack server execution is not supported: servers[${index}] is not an object`,
      );
    try {
      assertExactKeys(raw, serverKeys, `servers[${index}]`);
      const serverId = requireString(raw.id, `servers[${index}].id`);
      if (!idPattern.test(serverId))
        throw new Error(`Language pack server id is invalid: ${serverId}`);
      if (
        typeof raw.statusOrder !== "number" ||
        !Number.isInteger(raw.statusOrder) ||
        raw.statusOrder <= 0
      ) {
        throw new Error(
          `Language pack servers[${index}].statusOrder must be a positive integer`,
        );
      }
      const serverLanguages = requireStringArray(
        raw.languages,
        `servers[${index}].languages`,
      );
      if (serverLanguages.length === 0)
        throw new Error(`Language pack server ${serverId} needs a language`);
      const aliases = requireStringArray(
        raw.aliases,
        `servers[${index}].aliases`,
      );
      const allLanguageIds = [...serverLanguages, ...aliases];
      if (
        new Set(allLanguageIds).size !== allLanguageIds.length ||
        allLanguageIds.some((languageId) => !languageIds.has(languageId))
      ) {
        throw new Error(
          `Language pack server ${serverId} references duplicate or unknown languages`,
        );
      }
      const executablesRaw = raw.executables;
      if (!Array.isArray(executablesRaw) || executablesRaw.length === 0)
        throw new Error(
          `Language pack server ${serverId} needs an executable candidate`,
        );
      const executableIdentities = new Set<string>();
      const executables = executablesRaw.map(
        (executableRaw, executableIndex) => {
          if (!isRecord(executableRaw))
            throw new Error(
              `Language pack servers[${index}].executables[${executableIndex}] must be an object`,
            );
          assertExactKeys(
            executableRaw,
            executableKeys,
            `servers[${index}].executables[${executableIndex}]`,
          );
          const commandName = requireString(
            executableRaw.commandName,
            `servers[${index}].executables[${executableIndex}].commandName`,
          );
          const kind = requireString(
            executableRaw.kind,
            `servers[${index}].executables[${executableIndex}].kind`,
          );
          if (!commandPattern.test(commandName) || !idPattern.test(kind))
            throw new Error(
              `Language pack server ${serverId} has an unsafe executable candidate`,
            );
          const identity = `${commandName}\0${kind}`;
          if (executableIdentities.has(identity))
            throw new Error(
              `Language pack server ${serverId} repeats an executable candidate`,
            );
          executableIdentities.add(identity);
          return { commandName, kind };
        },
      );
      const args = requireStringArray(raw.args, `servers[${index}].args`);
      const installHint = requireString(
        raw.installHint,
        `servers[${index}].installHint`,
      );
      const workspaceNode = requireBoolean(
        raw.workspaceNode,
        `servers[${index}].workspaceNode`,
      );
      const initializationProfile = requireString(
        raw.initializationProfile,
        `servers[${index}].initializationProfile`,
      );
      if (
        initializationProfile !== "go" &&
        initializationProfile !== "typescript" &&
        initializationProfile !== "generic"
      )
        throw new Error(
          `Language pack server ${serverId} has an unsupported initialization profile`,
        );
      const configurationSections = requireStringArray(
        raw.configurationSections,
        `servers[${index}].configurationSections`,
      );
      if (configurationSections.length === 0)
        throw new Error(
          `Language pack server ${serverId} needs a configuration section`,
        );
      const configurationResponse = requireString(
        raw.configurationResponse,
        `servers[${index}].configurationResponse`,
      );
      if (
        configurationResponse !== "full" &&
        configurationResponse !== "preferences"
      )
        throw new Error(
          `Language pack server ${serverId} has an unsupported configuration response`,
        );
      const versionExecutable =
        raw.versionExecutable === undefined
          ? undefined
          : requireString(
              raw.versionExecutable,
              `servers[${index}].versionExecutable`,
            );
      if (
        versionExecutable !== undefined &&
        !commandPattern.test(versionExecutable)
      ) {
        throw new Error(
          `Language pack server ${serverId} has an unsafe version executable`,
        );
      }
      const versionArgs = requireStringArray(
        raw.versionArgs,
        `servers[${index}].versionArgs`,
      );
      const versionPin =
        raw.versionPin === undefined
          ? undefined
          : requireString(raw.versionPin, `servers[${index}].versionPin`);
      if (versionPin !== undefined && !semverPattern.test(versionPin)) {
        throw new Error(
          `Language pack server ${serverId} has an invalid version pin`,
        );
      }
      const preferReactWorkspace = requireBoolean(
        raw.preferReactWorkspace,
        `servers[${index}].preferReactWorkspace`,
      );
      const reactAware = requireBoolean(
        raw.reactAware,
        `servers[${index}].reactAware`,
      );
      for (const arg of [...args, ...versionArgs]) {
        if (arg.includes("\0"))
          throw new Error(
            `Language pack server ${serverId} contains a NUL argument`,
          );
      }
      return {
        id: serverId,
        statusOrder: raw.statusOrder,
        languages: serverLanguages,
        aliases,
        executables,
        args,
        installHint,
        workspaceNode,
        initializationProfile,
        configurationSections,
        configurationResponse,
        ...(versionExecutable ? { versionExecutable } : {}),
        versionArgs,
        ...(versionPin ? { versionPin } : {}),
        preferReactWorkspace,
        reactAware,
      } as LanguagePackServer;
    } catch (error) {
      throw new Error(
        `Language pack server execution is not supported: ${error instanceof Error ? error.message : String(error)}`,
      );
    }
  });

  const serverIds = new Set<string>();
  const serverOrders = new Set<number>();
  const configurationSections = new Set<string>();
  const serverLanguageOwners = new Map<string, string>();
  for (const server of servers) {
    if (serverIds.has(server.id))
      throw new Error(`Duplicate language pack server id: ${server.id}`);
    if (serverOrders.has(server.statusOrder))
      throw new Error(
        `Duplicate language pack server order: ${server.statusOrder}`,
      );
    serverIds.add(server.id);
    serverOrders.add(server.statusOrder);
    for (const languageId of [...server.languages, ...server.aliases]) {
      const owner = serverLanguageOwners.get(languageId);
      if (owner)
        throw new Error(
          `Language ${languageId} is assigned to both ${owner} and ${server.id}`,
        );
      serverLanguageOwners.set(languageId, server.id);
    }
    for (const section of server.configurationSections) {
      if (configurationSections.has(section))
        throw new Error(
          `Duplicate language pack configuration section: ${section}`,
        );
      configurationSections.add(section);
    }
  }

  return freezeManifest({
    schemaVersion: LANGUAGE_PACK_SCHEMA_VERSION,
    id,
    version,
    displayName,
    compatibility,
    languages,
    rootMarkers,
    servers,
    debuggers,
    toolchain,
    permissions,
    configurationSchema: {},
    integrity: { manifestSha256: input.integrity.manifestSha256 },
  });
}

export class LanguagePackRegistry {
  private readonly manifests = new Map<string, LanguagePackManifest>();
  private readonly packIDs = new Set<string>();
  private readonly languageOwners = new Map<string, string>();
  private readonly selectorOwners = new Map<string, string>();
  private readonly selectorLanguages = new Map<string, string>();

  register(input: unknown): LanguagePackManifest {
    const manifest = validateLanguagePackManifest(input);
    this.registerSelectors(manifest.id, manifest.languages);
    this.manifests.set(manifest.id, manifest);
    return manifest;
  }

  registerRuntimeContribution(input: unknown): LanguagePackRuntimeContribution {
    const contribution = validateLanguagePackRuntimeContribution(input);
    this.registerSelectors(contribution.id, contribution.languages);
    return contribution;
  }

  private registerSelectors(
    packID: string,
    languages: readonly LanguagePackLanguage[],
  ): void {
    if (this.packIDs.has(packID))
      throw new Error(`Language pack is already registered: ${packID}`);
    for (const language of languages) {
      const owner = this.languageOwners.get(language.id);
      if (owner)
        throw new Error(
          `Language id ${language.id} is already provided by ${owner}`,
        );
      for (const extension of language.extensions) {
        const selector = `extension:${extension.toLowerCase()}`;
        const selectorOwner = this.selectorOwners.get(selector);
        if (selectorOwner)
          throw new Error(
            `Language extension ${extension} is already provided by ${selectorOwner}`,
          );
      }
      for (const filename of language.filenames) {
        const selector = `filename:${filename.toLowerCase()}`;
        const selectorOwner = this.selectorOwners.get(selector);
        if (selectorOwner)
          throw new Error(
            `Language filename ${filename} is already provided by ${selectorOwner}`,
          );
      }
    }
    this.packIDs.add(packID);
    for (const language of languages) {
      this.languageOwners.set(language.id, packID);
      for (const extension of language.extensions) {
        const selector = `extension:${extension.toLowerCase()}`;
        this.selectorOwners.set(selector, packID);
        this.selectorLanguages.set(selector, language.id);
      }
      for (const filename of language.filenames) {
        const selector = `filename:${filename.toLowerCase()}`;
        this.selectorOwners.set(selector, packID);
        this.selectorLanguages.set(selector, language.id);
      }
    }
  }

  list(): readonly LanguagePackManifest[] {
    return [...this.manifests.values()];
  }

  detect(filePath: string): string | null {
    const fileName = (filePath.split(/[/\\]/).pop() ?? filePath).toLowerCase();
    const filenameLanguage = this.selectorLanguages.get(`filename:${fileName}`);
    if (filenameLanguage) return filenameLanguage;
    const dot = fileName.lastIndexOf(".");
    const extension = dot >= 0 ? fileName.slice(dot) : "";
    return extension
      ? (this.selectorLanguages.get(`extension:${extension}`) ?? null)
      : null;
  }
}

export function createBuiltInLanguagePackRegistry(): LanguagePackRegistry {
  const registry = new LanguagePackRegistry();
  registry.register(goLanguagePack);
  registry.register(typescriptLanguagePack);
  return registry;
}
