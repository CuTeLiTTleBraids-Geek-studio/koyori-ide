// Koyori IDE 模块 · Workspace；交互服务：文件系统（FileService）、项目（ProjectService）、Computer Use（ComputerUseService）。
// 喵，这是 Koyori IDE 的 Workspace 模块（前端实现）~
import * as FileServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/fileservice.js";
import * as ProjectServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/projectservice.js";
import * as SettingsServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/settingsservice.js";
import * as WindowServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/windowservice.js";
import * as TerminalServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/terminalservice.js";
import * as ComputerUseServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/computeruseservice.js";
import type { CreateProjectRequest, Project, Settings } from "@/types";
import {
  decodeWailsBytes, encodeWailsBytes, isRecord,
  optionalBoolean, optionalFiniteNumber, optionalInteger, optionalString,
  optionalStringRecord, optionalUnknownRecord, requiredBoolean,
  requiredFiniteNumber, requiredInteger, requiredString, safeRecordFromEntries,
  unwrapNullable, warnInvalidBoundaryValue,
} from "./boundary";

export const fileService = {
  listDirectory: (path: string) =>
    unwrapNullable(FileServiceBindings.ListDirectory(path), []),
  // Plan 55: Quick Open — recursive file listing respecting .gitignore.
  listAllFiles: (rootPath: string) =>
    unwrapNullable(FileServiceBindings.ListAllFiles(rootPath), []),
  readFile: (path: string) =>
    FileServiceBindings.ReadFile(path) as Promise<string>,
  writeFile: (path: string, content: string) =>
    FileServiceBindings.WriteFile(path, content) as Promise<void>,
  writeFileIfUnchanged: (path: string, content: string, baselineHash: string) =>
    FileServiceBindings.WriteFileIfUnchanged(path, content, baselineHash) as Promise<void>,
  createFile: (path: string) =>
    FileServiceBindings.CreateFile(path) as Promise<void>,
  createDirectory: (path: string) =>
    FileServiceBindings.CreateDirectory(path) as Promise<void>,
  deletePath: (path: string) =>
    FileServiceBindings.DeletePath(path) as Promise<void>,
  renamePath: (oldPath: string, newPath: string) =>
    FileServiceBindings.RenamePath(oldPath, newPath) as Promise<void>,
  pickDirectory: () =>
    FileServiceBindings.PickDirectory() as Promise<string>,
  revealInOS: (path: string) =>
    FileServiceBindings.RevealInOS(path) as Promise<void>,
};

export const computerUseService = {
  getConfig: () => ComputerUseServiceBindings.GetConfig(),
  updateConfig: (cfg: Parameters<typeof ComputerUseServiceBindings.UpdateConfig>[0]) =>
    ComputerUseServiceBindings.UpdateConfig(cfg),
  isEnabled: () => ComputerUseServiceBindings.IsEnabled(),
  getAuditLog: (limit: number) =>
    unwrapNullable(ComputerUseServiceBindings.GetAuditLog(limit), []),
  requestOperationApproval: (action: string, details: string) =>
    ComputerUseServiceBindings.RequestOperationApproval(action, details),
  executeApprovedOperation: (token: string) =>
    ComputerUseServiceBindings.ExecuteApprovedOperation(token),
  startRecording: () => ComputerUseServiceBindings.StartRecording(),
  stopRecording: () =>
    unwrapNullable(ComputerUseServiceBindings.StopRecording(), []),
  isRecording: () => ComputerUseServiceBindings.IsRecording(),
};

type BindingProject = Awaited<ReturnType<typeof ProjectServiceBindings.AddProject>>;
type BindingWorkspaceSnapshot = Awaited<ReturnType<typeof ProjectServiceBindings.GetWorkspaceSnapshot>>;

export interface WorkspaceAuthoritySnapshot {
  root: string;
  roots: string[];
  generation: number;
  projectId?: string;
  projectName?: string;
  projectPath?: string;
}

function fromBindingProject(project: BindingProject): Project {
  return {
    ...project,
    roots: project.roots ?? undefined,
    remote: project.remote ?? undefined,
  };
}

function fromBindingWorkspaceSnapshot(
  snapshot: BindingWorkspaceSnapshot,
): WorkspaceAuthoritySnapshot {
  return {
    ...snapshot,
    roots: snapshot.roots ?? [],
  };
}

export const projectService = {
  getRecentProjects: async () => {
    const projects = await unwrapNullable(ProjectServiceBindings.GetRecentProjects(), []);
    return projects.map(fromBindingProject);
  },
  addProject: async (path: string) =>
    fromBindingProject(await ProjectServiceBindings.AddProject(path)),
  addMultiRootProject: async (roots: string[], workspacePath: string) =>
    fromBindingProject(await ProjectServiceBindings.AddMultiRootProject(roots, workspacePath)),
  getWorkspaceSnapshot: async () =>
    fromBindingWorkspaceSnapshot(await ProjectServiceBindings.GetWorkspaceSnapshot()),
  removeProject: (id: string) =>
    ProjectServiceBindings.RemoveProject(id) as Promise<void>,
  // G-FEAT-01: New Project scaffolding wizard.
  listProjectTemplates: () =>
    unwrapNullable(ProjectServiceBindings.ListProjectTemplates(), []),
  createProject: (req: CreateProjectRequest) =>
    ProjectServiceBindings.CreateProject(req),
};

type BindingSettings = Parameters<typeof SettingsServiceBindings.SaveSettings>[0];
type BindingAIProviderConfig = NonNullable<BindingSettings["aiProviderConfigs"]>[number];
type FrontendAIProviderConfig = NonNullable<Settings["aiProviderConfigs"]>[number];
type FrontendPersonalization = NonNullable<Settings["personalization"]>;
type FrontendCustomAccent = NonNullable<Settings["customAccent"]>;

function normalizeAIWindowTheme(
  value: unknown,
  path: string,
): NonNullable<Settings["aiWindowTheme"]> {
  switch (value) {
    case "apple-dark":
    case "apple-light":
    case "claude-dark":
    case "claude-light":
    case "system":
      return value;
    default:
      warnInvalidBoundaryValue(path, "a supported AI window theme", '"apple-dark"');
      return "apple-dark";
  }
}

function normalizeAIChatPosition(
  value: unknown,
  path: string,
): Settings["aiChatPosition"] {
  if (value === undefined || value === null || value === "") return undefined;
  if (value === "left" || value === "right") return value;
  warnInvalidBoundaryValue(path, '"left" or "right"', "undefined");
  return undefined;
}

function normalizeToolApprovalConfig(
  value: unknown,
  path: string,
): Settings["toolApprovalConfig"] {
  if (value === undefined || value === null) return undefined;
  if (!isRecord(value)) {
    warnInvalidBoundaryValue(path, "an approval-policy object", "undefined");
    return undefined;
  }
  const entries: Array<[
    string,
    NonNullable<Settings["toolApprovalConfig"]>[string],
  ]> = [];
  let invalid = false;
  for (const [key, policy] of Object.entries(value)) {
    if (
      policy === "always-ask" ||
      policy === "auto-approve" ||
      policy === "never-approve"
    ) {
      entries.push([key, policy]);
    } else {
      invalid = true;
    }
  }
  if (invalid) {
    warnInvalidBoundaryValue(path, "an approval-policy object", "valid entries only");
  }
  return safeRecordFromEntries(entries);
}

function normalizeShortcutMap(
  value: unknown,
  path: string,
): Settings["customShortcuts"] {
  if (value === undefined || value === null) return undefined;
  if (!isRecord(value)) {
    warnInvalidBoundaryValue(path, "a shortcut object", "undefined");
    return undefined;
  }
  const entries: Array<[
    string,
    NonNullable<Settings["customShortcuts"]>[string],
  ]> = [];
  for (const [label, shortcut] of Object.entries(value)) {
    if (!isRecord(shortcut)) {
      warnInvalidBoundaryValue(`${path}.${label}`, "a shortcut object", "omitted");
      continue;
    }
    entries.push([label, {
      key: requiredString(shortcut.key, `${path}.${label}.key`),
      ctrl: requiredBoolean(shortcut.ctrl, `${path}.${label}.ctrl`, false),
      shift: requiredBoolean(shortcut.shift, `${path}.${label}.shift`, false),
      alt: requiredBoolean(shortcut.alt, `${path}.${label}.alt`, false),
    }]);
  }
  return safeRecordFromEntries(entries);
}

function normalizeCustomAccent(
  value: unknown,
  path: string,
): FrontendCustomAccent | null | undefined {
  if (value === undefined) return undefined;
  if (value === null) return null;
  if (!isRecord(value)) {
    warnInvalidBoundaryValue(path, "a custom accent object", "undefined");
    return undefined;
  }
  return {
    name: requiredString(value.name, `${path}.name`),
    color: requiredString(value.color, `${path}.color`, "#007aff"),
    primary: optionalString(value.primary, `${path}.primary`),
    primaryHover: optionalString(value.primaryHover, `${path}.primaryHover`),
    primaryLight: optionalString(value.primaryLight, `${path}.primaryLight`),
    primaryContainer: optionalString(value.primaryContainer, `${path}.primaryContainer`),
    onPrimary: optionalString(value.onPrimary, `${path}.onPrimary`),
    onPrimaryContainer: optionalString(
      value.onPrimaryContainer,
      `${path}.onPrimaryContainer`,
    ),
  };
}

function normalizePersonalization(
  value: unknown,
  path: string,
): FrontendPersonalization | undefined {
  if (value === undefined || value === null) return undefined;
  if (!isRecord(value)) {
    warnInvalidBoundaryValue(path, "a personalization object", "undefined");
    return undefined;
  }
  return {
    codeEditorBgImage: optionalString(value.codeEditorBgImage, `${path}.codeEditorBgImage`),
    codeEditorBgOpacity: optionalFiniteNumber(
      value.codeEditorBgOpacity,
      `${path}.codeEditorBgOpacity`,
    ),
    codeEditorBgBlur: optionalFiniteNumber(value.codeEditorBgBlur, `${path}.codeEditorBgBlur`),
    chatBgImage: optionalString(value.chatBgImage, `${path}.chatBgImage`),
    chatBgOpacity: optionalFiniteNumber(value.chatBgOpacity, `${path}.chatBgOpacity`),
    chatBgBlur: optionalFiniteNumber(value.chatBgBlur, `${path}.chatBgBlur`),
    userAvatar: optionalString(value.userAvatar, `${path}.userAvatar`),
    aiAvatar: optionalString(value.aiAvatar, `${path}.aiAvatar`),
    personaAvatars: optionalStringRecord(value.personaAvatars, `${path}.personaAvatars`),
    fontFamily: optionalString(value.fontFamily, `${path}.fontFamily`),
    fontSize: optionalInteger(value.fontSize, `${path}.fontSize`),
    bubbleStyle:
      value.bubbleStyle === "rounded" ||
      value.bubbleStyle === "sharp" ||
      value.bubbleStyle === "bubble"
        ? value.bubbleStyle
        : undefined,
    bubbleOpacity: optionalFiniteNumber(value.bubbleOpacity, `${path}.bubbleOpacity`),
    messageSpacing: optionalInteger(value.messageSpacing, `${path}.messageSpacing`),
  };
}

function fromBindingAIProviderConfig(
  value: unknown,
  path: string,
): FrontendAIProviderConfig {
  const config = isRecord(value) ? value : {};
  if (!isRecord(value)) {
    warnInvalidBoundaryValue(path, "an AI provider config", "an empty config");
  }
  return {
    id: requiredString(config.id, `${path}.id`),
    name: requiredString(config.name, `${path}.name`),
    provider: requiredString(config.provider, `${path}.provider`),
    protocol: optionalString(config.protocol, `${path}.protocol`),
    apiKey: requiredString(config.apiKey, `${path}.apiKey`),
    apiKeyConfigured: optionalBoolean(config.apiKeyConfigured, `${path}.apiKeyConfigured`),
    baseUrl: requiredString(config.baseUrl, `${path}.baseUrl`),
    model: requiredString(config.model, `${path}.model`),
    temperature: optionalFiniteNumber(config.temperature, `${path}.temperature`),
    maxTokens: optionalInteger(config.maxTokens, `${path}.maxTokens`),
    systemPrompt: optionalString(config.systemPrompt, `${path}.systemPrompt`),
  };
}

function toBindingAIProviderConfig(
  value: unknown,
  path: string,
): BindingAIProviderConfig {
  const config = isRecord(value) ? value : {};
  if (!isRecord(value)) {
    warnInvalidBoundaryValue(path, "an AI provider config", "an empty config");
  }
  return {
    id: requiredString(config.id, `${path}.id`),
    name: requiredString(config.name, `${path}.name`),
    provider: requiredString(config.provider, `${path}.provider`),
    protocol: optionalString(config.protocol, `${path}.protocol`),
    apiKey: requiredString(config.apiKey, `${path}.apiKey`),
    apiKeyConfigured: optionalBoolean(config.apiKeyConfigured, `${path}.apiKeyConfigured`),
    baseUrl: requiredString(config.baseUrl, `${path}.baseUrl`),
    model: requiredString(config.model, `${path}.model`),
    temperature: optionalFiniteNumber(config.temperature, `${path}.temperature`),
    maxTokens: optionalInteger(config.maxTokens, `${path}.maxTokens`),
    systemPrompt: optionalString(config.systemPrompt, `${path}.systemPrompt`),
  };
}

function normalizeAIProviderConfigs<T>(
  value: unknown,
  path: string,
  convert: (config: unknown, path: string) => T,
): T[] | undefined {
  if (value === undefined || value === null) return undefined;
  if (!Array.isArray(value)) {
    warnInvalidBoundaryValue(path, "an array of AI provider configs", "undefined");
    return undefined;
  }
  const normalized: T[] = [];
  value.forEach((entry, index) => {
    if (!isRecord(entry)) {
      warnInvalidBoundaryValue(`${path}[${index}]`, "an AI provider config", "omitted");
      return;
    }
    normalized.push(convert(entry, `${path}[${index}]`));
  });
  return normalized;
}

/** Convert generated Wails settings into the frontend DTO with legacy-safe defaults. */
export function fromBindingSettings(settings: BindingSettings): Settings {
  return {
    schemaVersion: requiredInteger(settings.schemaVersion, "Settings.schemaVersion", 1),
    version: requiredInteger(settings.version, "Settings.version", 0),
    expectedVersion: optionalInteger(settings.expectedVersion, "Settings.expectedVersion"),
    language: requiredString(settings.language, "Settings.language", "en"),
    theme: requiredString(settings.theme, "Settings.theme", "dark"),
    fontSize: requiredInteger(settings.fontSize, "Settings.fontSize", 14),
    fontFamily: requiredString(settings.fontFamily, "Settings.fontFamily", "JetBrains Mono"),
    tabSize: requiredInteger(settings.tabSize, "Settings.tabSize", 2),
    wordWrap: requiredBoolean(settings.wordWrap, "Settings.wordWrap", true),
    lineNumbers: requiredBoolean(settings.lineNumbers, "Settings.lineNumbers", true),
    minimap: requiredBoolean(settings.minimap, "Settings.minimap", false),
    aiApiKey: requiredString(settings.aiApiKey, "Settings.aiApiKey"),
    aiApiKeyConfigured: requiredBoolean(
      settings.aiApiKeyConfigured,
      "Settings.aiApiKeyConfigured",
      false,
    ),
    aiApiKeyStorageMethod: requiredString(
      settings.aiApiKeyStorageMethod,
      "Settings.aiApiKeyStorageMethod",
      "none",
    ),
    aiBaseUrl: requiredString(settings.aiBaseUrl, "Settings.aiBaseUrl", "https://api.openai.com"),
    aiModel: requiredString(settings.aiModel, "Settings.aiModel", "gpt-4o"),
    aiSystemPrompt: requiredString(settings.aiSystemPrompt, "Settings.aiSystemPrompt"),
    aiAgentSystemPrompt: optionalString(
      settings.aiAgentSystemPrompt,
      "Settings.aiAgentSystemPrompt",
    ),
    aiConversationTitlePrompt: optionalString(
      settings.aiConversationTitlePrompt,
      "Settings.aiConversationTitlePrompt",
    ),
    aiInlineCompletionPrompt: optionalString(
      settings.aiInlineCompletionPrompt,
      "Settings.aiInlineCompletionPrompt",
    ),
    cursorBlinking: requiredString(settings.cursorBlinking, "Settings.cursorBlinking", "blink"),
    cursorStyle: requiredString(settings.cursorStyle, "Settings.cursorStyle", "line"),
    bracketColorization: requiredBoolean(
      settings.bracketColorization,
      "Settings.bracketColorization",
      true,
    ),
    autoSave: requiredBoolean(settings.autoSave, "Settings.autoSave", false),
    autoSaveDelay: requiredString(settings.autoSaveDelay, "Settings.autoSaveDelay", "afterDelay"),
    aiProvider: requiredString(settings.aiProvider, "Settings.aiProvider"),
    temperature: requiredFiniteNumber(settings.temperature, "Settings.temperature", 0.7),
    maxTokens: requiredInteger(settings.maxTokens, "Settings.maxTokens", 4096),
    defaultShell: requiredString(settings.defaultShell, "Settings.defaultShell"),
    terminalFontSize: requiredInteger(settings.terminalFontSize, "Settings.terminalFontSize", 13),
    terminalCursorStyle: requiredString(
      settings.terminalCursorStyle,
      "Settings.terminalCursorStyle",
      "block",
    ),
    scrollback: requiredInteger(settings.scrollback, "Settings.scrollback", 10000),
    uiDensity: requiredString(settings.uiDensity, "Settings.uiDensity", "comfortable"),
    fontSizeScaling: requiredInteger(settings.fontSizeScaling, "Settings.fontSizeScaling", 100),
    inlineCompletionEnabled: requiredBoolean(
      settings.inlineCompletionEnabled,
      "Settings.inlineCompletionEnabled",
      true,
    ),
    formatOnSave: requiredBoolean(settings.formatOnSave, "Settings.formatOnSave", true),
    trimTrailingWhitespace: requiredBoolean(
      settings.trimTrailingWhitespace,
      "Settings.trimTrailingWhitespace",
      false,
    ),
    insertSpaces: requiredBoolean(settings.insertSpaces, "Settings.insertSpaces", true),
    insertFinalNewline: requiredBoolean(
      settings.insertFinalNewline,
      "Settings.insertFinalNewline",
      true,
    ),
    gitBlameEnabled: requiredBoolean(settings.gitBlameEnabled, "Settings.gitBlameEnabled", false),
    emmetEnabled: requiredBoolean(settings.emmetEnabled, "Settings.emmetEnabled", true),
    emmetIncludeLanguages: optionalStringRecord(
      settings.emmetIncludeLanguages,
      "Settings.emmetIncludeLanguages",
    ),
    customShortcuts: normalizeShortcutMap(settings.customShortcuts, "Settings.customShortcuts"),
    aiChatPosition: normalizeAIChatPosition(settings.aiChatPosition, "Settings.aiChatPosition"),
    activityBarVisible: requiredBoolean(
      settings.activityBarVisible,
      "Settings.activityBarVisible",
      true,
    ),
    toolApprovalConfig: normalizeToolApprovalConfig(
      settings.toolApprovalConfig,
      "Settings.toolApprovalConfig",
    ),
    accentTheme: optionalString(settings.accentTheme, "Settings.accentTheme"),
    customAccent: normalizeCustomAccent(settings.customAccent, "Settings.customAccent"),
    enablePluginSandbox: requiredBoolean(
      settings.enablePluginSandbox,
      "Settings.enablePluginSandbox",
      true,
    ),
    aiProviderConfigs: normalizeAIProviderConfigs(
      settings.aiProviderConfigs,
      "Settings.aiProviderConfigs",
      fromBindingAIProviderConfig,
    ),
    activeAIConfigId: optionalString(settings.activeAIConfigId, "Settings.activeAIConfigId"),
    toolPaths: optionalStringRecord(settings.toolPaths, "Settings.toolPaths"),
    personalization: normalizePersonalization(settings.personalization, "Settings.personalization"),
    openAIWindowOnStartup: requiredBoolean(
      settings.openAIWindowOnStartup,
      "Settings.openAIWindowOnStartup",
      false,
    ),
    aiWindowTheme: normalizeAIWindowTheme(settings.aiWindowTheme, "Settings.aiWindowTheme"),
    aiSidebarWidth: requiredInteger(settings.aiSidebarWidth, "Settings.aiSidebarWidth", 288),
    aiTerminalWidth: requiredInteger(settings.aiTerminalWidth, "Settings.aiTerminalWidth", 440),
    lspConfigs: optionalUnknownRecord(settings.lspConfigs, "Settings.lspConfigs"),
  };
}

/** Convert the frontend settings DTO into the exact generated Wails payload. */
export function toBindingSettings(settings: Settings): BindingSettings {
  return {
    schemaVersion: requiredInteger(settings.schemaVersion, "Settings.schemaVersion", 1),
    version: requiredInteger(settings.version, "Settings.version", 0),
    expectedVersion: optionalInteger(settings.expectedVersion, "Settings.expectedVersion"),
    language: requiredString(settings.language, "Settings.language", "en"),
    theme: requiredString(settings.theme, "Settings.theme", "dark"),
    fontSize: requiredInteger(settings.fontSize, "Settings.fontSize", 14),
    fontFamily: requiredString(settings.fontFamily, "Settings.fontFamily", "JetBrains Mono"),
    tabSize: requiredInteger(settings.tabSize, "Settings.tabSize", 2),
    wordWrap: requiredBoolean(settings.wordWrap, "Settings.wordWrap", true),
    lineNumbers: requiredBoolean(settings.lineNumbers, "Settings.lineNumbers", true),
    minimap: requiredBoolean(settings.minimap, "Settings.minimap", false),
    aiApiKey: requiredString(settings.aiApiKey, "Settings.aiApiKey"),
    aiBaseUrl: requiredString(settings.aiBaseUrl, "Settings.aiBaseUrl", "https://api.openai.com"),
    aiModel: requiredString(settings.aiModel, "Settings.aiModel", "gpt-4o"),
    aiSystemPrompt: requiredString(settings.aiSystemPrompt, "Settings.aiSystemPrompt"),
    aiApiKeyConfigured: requiredBoolean(
      settings.aiApiKeyConfigured,
      "Settings.aiApiKeyConfigured",
      false,
    ),
    aiApiKeyStorageMethod: requiredString(
      settings.aiApiKeyStorageMethod,
      "Settings.aiApiKeyStorageMethod",
      "none",
    ),
    aiAgentSystemPrompt: optionalString(
      settings.aiAgentSystemPrompt,
      "Settings.aiAgentSystemPrompt",
    ),
    aiConversationTitlePrompt: optionalString(
      settings.aiConversationTitlePrompt,
      "Settings.aiConversationTitlePrompt",
    ),
    aiInlineCompletionPrompt: optionalString(
      settings.aiInlineCompletionPrompt,
      "Settings.aiInlineCompletionPrompt",
    ),
    cursorBlinking: requiredString(settings.cursorBlinking, "Settings.cursorBlinking", "blink"),
    cursorStyle: requiredString(settings.cursorStyle, "Settings.cursorStyle", "line"),
    bracketColorization: requiredBoolean(
      settings.bracketColorization,
      "Settings.bracketColorization",
      true,
    ),
    autoSave: requiredBoolean(settings.autoSave, "Settings.autoSave", false),
    autoSaveDelay: requiredString(settings.autoSaveDelay, "Settings.autoSaveDelay", "afterDelay"),
    aiProvider: requiredString(settings.aiProvider, "Settings.aiProvider"),
    temperature: requiredFiniteNumber(settings.temperature, "Settings.temperature", 0.7),
    maxTokens: requiredInteger(settings.maxTokens, "Settings.maxTokens", 4096),
    defaultShell: requiredString(settings.defaultShell, "Settings.defaultShell"),
    terminalFontSize: requiredInteger(settings.terminalFontSize, "Settings.terminalFontSize", 13),
    terminalCursorStyle: requiredString(
      settings.terminalCursorStyle,
      "Settings.terminalCursorStyle",
      "block",
    ),
    scrollback: requiredInteger(settings.scrollback, "Settings.scrollback", 10000),
    uiDensity: requiredString(settings.uiDensity, "Settings.uiDensity", "comfortable"),
    fontSizeScaling: requiredInteger(settings.fontSizeScaling, "Settings.fontSizeScaling", 100),
    inlineCompletionEnabled: requiredBoolean(
      settings.inlineCompletionEnabled,
      "Settings.inlineCompletionEnabled",
      true,
    ),
    formatOnSave: requiredBoolean(settings.formatOnSave, "Settings.formatOnSave", true),
    trimTrailingWhitespace: requiredBoolean(
      settings.trimTrailingWhitespace,
      "Settings.trimTrailingWhitespace",
      false,
    ),
    insertSpaces: requiredBoolean(settings.insertSpaces, "Settings.insertSpaces", true),
    insertFinalNewline: requiredBoolean(
      settings.insertFinalNewline,
      "Settings.insertFinalNewline",
      true,
    ),
    gitBlameEnabled: requiredBoolean(settings.gitBlameEnabled, "Settings.gitBlameEnabled", false),
    emmetEnabled: requiredBoolean(settings.emmetEnabled, "Settings.emmetEnabled", true),
    emmetIncludeLanguages: optionalStringRecord(
      settings.emmetIncludeLanguages,
      "Settings.emmetIncludeLanguages",
    ),
    customShortcuts: normalizeShortcutMap(settings.customShortcuts, "Settings.customShortcuts"),
    aiChatPosition: normalizeAIChatPosition(settings.aiChatPosition, "Settings.aiChatPosition"),
    activityBarVisible: requiredBoolean(
      settings.activityBarVisible,
      "Settings.activityBarVisible",
      true,
    ),
    toolApprovalConfig: normalizeToolApprovalConfig(
      settings.toolApprovalConfig,
      "Settings.toolApprovalConfig",
    ),
    accentTheme: optionalString(settings.accentTheme, "Settings.accentTheme"),
    customAccent: normalizeCustomAccent(settings.customAccent, "Settings.customAccent"),
    enablePluginSandbox: requiredBoolean(
      settings.enablePluginSandbox,
      "Settings.enablePluginSandbox",
      true,
    ),
    aiProviderConfigs: normalizeAIProviderConfigs(
      settings.aiProviderConfigs,
      "Settings.aiProviderConfigs",
      toBindingAIProviderConfig,
    ),
    activeAIConfigId: optionalString(settings.activeAIConfigId, "Settings.activeAIConfigId"),
    toolPaths: optionalStringRecord(settings.toolPaths, "Settings.toolPaths"),
    personalization: normalizePersonalization(settings.personalization, "Settings.personalization"),
    openAIWindowOnStartup: requiredBoolean(
      settings.openAIWindowOnStartup,
      "Settings.openAIWindowOnStartup",
      false,
    ),
    aiWindowTheme: normalizeAIWindowTheme(settings.aiWindowTheme, "Settings.aiWindowTheme"),
    aiSidebarWidth: requiredInteger(settings.aiSidebarWidth, "Settings.aiSidebarWidth", 288),
    aiTerminalWidth: requiredInteger(settings.aiTerminalWidth, "Settings.aiTerminalWidth", 440),
    lspConfigs: optionalUnknownRecord(settings.lspConfigs, "Settings.lspConfigs"),
  };
}

export const settingsService = {
  loadSettings: async () =>
    fromBindingSettings(await SettingsServiceBindings.LoadSettings()),
  saveSettings: (settings: Settings) =>
    SettingsServiceBindings.SaveSettings(toBindingSettings(settings)),
  isAPIKeyEncryptedOnDisk: () =>
    SettingsServiceBindings.IsAPIKeyEncryptedOnDisk() as Promise<boolean>,
  getAPIKeyStorageMethod: () =>
    SettingsServiceBindings.GetAPIKeyStorageMethod() as Promise<string>,
  // N-49: read-only keyring inventory; renderer deletion is intentionally hidden.
  listSecrets: () =>
    unwrapNullable(SettingsServiceBindings.ListSecrets(), []),
  // Plan 11 Task 15: personalization asset storage (image upload/read/delete).
  // Wails bindings type Go []byte as string; cast at the boundary.
  savePersonalizationAsset: (filename: string, data: Uint8Array) =>
    SettingsServiceBindings.SavePersonalizationAsset(
      filename,
      encodeWailsBytes(data),
    ) as Promise<string>,
  readPersonalizationAsset: async (relPath: string): Promise<Uint8Array> => {
    const raw = await unwrapNullable(
      SettingsServiceBindings.ReadPersonalizationAsset(relPath),
      "",
    );
    return decodeWailsBytes(raw);
  },
  deletePersonalizationAsset: (relPath: string) =>
    SettingsServiceBindings.DeletePersonalizationAsset(relPath) as Promise<void>,
};

// prompt-6 Task 7: WindowService AI methods use the regenerated module; no
// renderer-owned runtime method name or numeric ID remains here.
export const windowService = {
  minimise: () => WindowServiceBindings.Minimise(),
  maximise: () => WindowServiceBindings.Maximise(),
  // N-152: 标题栏放大/还原按钮共用一个控件，调用 ToggleMaximise 切换状态。
  toggleMaximise: () => WindowServiceBindings.ToggleMaximise(),
  // 查询当前窗口是否已最大化（用于初始化图标状态）。
  isMaximised: () => WindowServiceBindings.IsMaximised() as Promise<boolean>,
  close: () => WindowServiceBindings.Close(),
  toggleFullscreen: () => WindowServiceBindings.ToggleFullscreen(),
  setTitle: (title: string) => WindowServiceBindings.SetTitle(title),
  // prompt-4 Task 1: AI 独立窗口管理
  openAIWindow: () => WindowServiceBindings.OpenAIWindow() as Promise<void>,
  closeAIWindow: () => WindowServiceBindings.CloseAIWindow() as Promise<void>,
  toggleAIWindow: () => WindowServiceBindings.ToggleAIWindow() as Promise<void>,
  isAIWindowOpen: () => WindowServiceBindings.IsAIWindowOpen() as Promise<boolean>,
  isAIWindowVisible: () => WindowServiceBindings.IsAIWindowVisible() as Promise<boolean>,
  setAIAlwaysOnTop: (onTop: boolean) =>
    WindowServiceBindings.SetAIAlwaysOnTop(onTop) as Promise<void>,
  isAIAlwaysOnTop: () => WindowServiceBindings.IsAIAlwaysOnTop() as Promise<boolean>,
  // BUG6: AI 窗口 Frameless 模式下的窗口控制方法。
  minimiseAIWindow: () => WindowServiceBindings.MinimiseAIWindow(),
  toggleMaximiseAIWindow: () => WindowServiceBindings.ToggleMaximiseAIWindow(),
  isAIWindowMaximised: () =>
    WindowServiceBindings.IsAIWindowMaximised() as Promise<boolean>,
  sendSelectionToAI: (code: string, language: string, filePath: string) =>
    WindowServiceBindings.SendSelectionToAI(code, language, filePath) as Promise<void>,
  openPathInExplorer: (path: string) =>
    WindowServiceBindings.OpenPathInExplorer(path) as Promise<void>,
  openPathInVSCode: (path: string) =>
    WindowServiceBindings.OpenPathInVSCode(path) as Promise<void>,
  focusMainWindow: () => WindowServiceBindings.FocusMainWindow() as Promise<void>,
};

export const terminalService = {
  start: (workingDir: string) =>
    TerminalServiceBindings.Start(workingDir) as Promise<void>,
  write: (input: string) =>
    TerminalServiceBindings.Write(input) as Promise<void>,
  kill: () => TerminalServiceBindings.Kill() as Promise<void>,
  isRunning: () =>
    TerminalServiceBindings.IsRunning() as Promise<boolean>,
  resize: (cols: number, rows: number) =>
    TerminalServiceBindings.Resize(cols, rows) as Promise<void>,
  startSession: (id: string, workingDir: string, shell: string) =>
    TerminalServiceBindings.StartSession(id, workingDir, shell) as Promise<void>,
  killSession: (id: string) =>
    TerminalServiceBindings.KillSession(id) as Promise<void>,
  writeSession: (id: string, input: string) =>
    TerminalServiceBindings.WriteSession(id, input) as Promise<void>,
  resizeSession: (id: string, cols: number, rows: number) =>
    TerminalServiceBindings.ResizeSession(id, cols, rows) as Promise<void>,
  isSessionRunning: (id: string) =>
    TerminalServiceBindings.IsSessionRunning(id) as Promise<boolean>,
  listSessions: () =>
    unwrapNullable(TerminalServiceBindings.ListSessions(), []),
};
