// F-10 (task-5.md) + 架构改进 A: app.ts 仅做聚合与兼容入口。
// appState 拆分为 themeStore/settingsStore/aiConfigStore/projectStore/windowStore
// 五个子 store，对外以 appState 聚合委托。行为层（loadSettings/saveSettings/
// 主题/监听器/AI 配置/toggle*/workspace）已迁移到 appActions/workspaceStore/
// layoutStore/connectivityStore。本文件 re-export 这些模块的公开符号，
// 旧代码 `import { xxx } from "@/stores/app"` 继续可用。
// Koyori IDE 模块 · App。
// 喵，这是 Koyori IDE 的 App 模块（前端实现）~
import { computed, reactive, watch, type WatchStopHandle } from "vue";
import type { ShortcutKeys, AgentPermissionMode, CustomAccentTheme, AIProviderConfig, PersonalizationConfig, AIWindowTheme, ReasoningEffort } from "@/types";
import type { AccentTheme } from "@/lib/monaco-themes";
import { accentThemes } from "@/lib/monaco-themes";
import {
  initAppActions,
  unregisterAppListeners,
} from "@/stores/appActions";

// Re-export theme editor helpers + 行为层 + 拆分后的 store（保持旧引用兼容）。
export { deriveAccentTokens, serializeCustomAccent, deserializeCustomAccent } from "@/stores/themeEditor";
export { resolveSystemMode, applyAccentTheme, applyMode, applyDesignLanguage, applyFontSizeScaling, applyUiDensity, initThemes, setCustomAccent, startSystemModeListener, stopSystemModeListener, initWindowMaximiseListener, handleWindowMaximisedEvent, stopWindowMaximiseListener, initSettingsSyncListener, handleSettingsChangedEvent, cleanupSettingsSyncListener, initProjectRemovedListener, handleProjectRemovedEvent, cleanupProjectRemovedListener, unregisterAppListeners, loadSettings, saveSettings, flushSettingsSave, generateAIConfigId, activateAIConfig, saveAIConfig, deleteAIConfig, createNewAIConfig, activeAIConfig, setPanelTab, setActiveExtensionView, setExtensionsSubview, initAppActions } from "@/stores/appActions";
export { workspaceStore, setWorkspaceFolders, addWorkspaceFolder, removeWorkspaceFolder, isCodeWorkspacePath, parseCodeWorkspaceContent, loadWorkspaceFolders, openProject, applyWorkspaceSnapshot, syncWorkspaceSnapshot, handleWorkspaceChangedEvent, resetWorkspaceAuthorityForTesting } from "@/stores/workspaceStore";
export { layoutState, activeLeaf, findLeaf, findLeafByViewId, findParent, countLeaves, collectLeaves, splitLeaf, closeLeaf, replaceLeafView, setActiveLeaf, resetLayout, serializeLayout, deserializeLayout, loadLayoutFromBackend, saveLayoutToBackend, resetLayoutFromBackend, layoutStore, setActivityBarVisible, setSidebarVisible, setPanelVisible, setAiChatVisible, setAiChatPosition, setAiSidebarWidth, setAiTerminalWidth, setStatusBarVisible, toggleSidebar, toggleTerminal, toggleAiChat, toggleAiChatPosition, toggleActivityBar, toggleStatusBar } from "@/stores/layoutStore";
export { connectivityState, checkAIReachable, initConnectivityListener, unregisterConnectivityListener, stopConnectivityListener, __resetConnectivityForTesting, __refreshOnlineStateForTesting, checkConnectivity, setOnlineStatus, type ConnectivityState } from "@/stores/connectivityStore";

export type PanelTab = "explorer" | "build" | "database" | "inspections" | "search" | "git" | "extensions" | "ai" | "callHierarchy" | "httpClient";
export type ThemeMode = "dark" | "light" | "system";
export type ExtensionsSubview = "installed" | "marketplace";

export interface AppState {
  // windowStore
  sidebarCollapsed: boolean;
  sidebarWidth: number;
  activityBarWidth: number;
  panelTab: PanelTab;
  extensionsSubview: ExtensionsSubview;
  activeExtensionView: string | null;
  terminalVisible: boolean;
  terminalHeight: number;
  aiChatVisible: boolean;
  aiChatWidth: number;
  statusBarVisible: boolean;
  breadcrumbVisible: boolean;
  activityBarVisible: boolean;
  isWindowMaximised: boolean;
  // projectStore
  currentFilePath: string | null;
  currentProject: string | null;
  projectName: string | null;
  workspaceRoot: string | null;
  workspaceFolders: string[];
  workspaceGeneration: number;
  branchName: string;
  errors: number;
  warnings: number;
  cursorLine: number;
  cursorColumn: number;
  editorJumpSeq: number;
  editorJumpTargetPath: string | null;
  editorJumpTargetGroupId: string | null;
  editorJumpTargetSeq: number;
  encoding: string;
  languageMode: string;
  bottomPanelView: "terminal" | "output" | "problems" | "debug" | "tasks" | "workflows" | "profile" | "";
  toolPaths: Record<string, string>;
  personalization: PersonalizationConfig;
  // themeStore
  theme: string;
  accentTheme: AccentTheme;
  customAccent: CustomAccentTheme | null;
  designLanguage: "apple" | "claude";
  fontSizeScaling: number;
  uiDensity: string;
  // settingsStore
  fontSize: number;
  fontFamily: string;
  tabSize: number;
  wordWrap: boolean;
  minimap: boolean;
  /** Compatibility alias for minimap, used by the editor-experience settings. */
  minimapEnabled: boolean;
  stickyScrollEnabled: boolean;
  inlayHintsEnabled: boolean;
  organizeImportsOnSave: boolean;
  lineNumbers: boolean;
  cursorBlinking: string;
  cursorStyle: string;
  bracketColorization: boolean;
  autoSave: boolean;
  autoSaveDelay: string;
  formatOnSave: boolean;
  trimTrailingWhitespace: boolean;
  insertSpaces: boolean;
  insertFinalNewline: boolean;
  gitBlameEnabled: boolean;
  emmetEnabled: boolean;
  emmetIncludeLanguages: Record<string, string>;
  language: string;
  autoUpdate: boolean;
  dataFolderPath: string;
  enablePluginSandbox: boolean;
  customShortcuts: Record<string, ShortcutKeys>;
  defaultShell: string;
  terminalFontSize: number;
  terminalCursorStyle: string;
  scrollback: number;
  settingsVersion: number;
  /** Per split-editor group tab membership and active file. */
  editorGroupFilePaths: Record<string, string[]>;
  editorGroupActiveFiles: Record<string, string | null>;
  // aiConfigStore
  aiApiKey: string;
  aiApiKeyConfigured: boolean;
  aiApiKeyStorageMethod: string;
  aiBaseUrl: string;
  aiModel: string;
  aiSystemPrompt: string;
  aiProvider: string;
  aiAgentSystemPrompt: string;
  aiConversationTitlePrompt: string;
  aiInlineCompletionPrompt: string;
  temperature: number;
  reasoningEffort: ReasoningEffort;
  maxTokens: number;
  aiChatPosition: "left" | "right";
  aiProviderConfigs: AIProviderConfig[];
  activeAIConfigId: string;
  agentPermissionMode: AgentPermissionMode;
  inlineCompletionEnabled: boolean;
  aiWindowTheme: AIWindowTheme;
  aiSidebarWidth: number;
  aiTerminalWidth: number;
  openAIWindowOnStartup: boolean;
}

// H-18: 5 个独立 sub-store。appState 作为向后兼容聚合，通过 getter/setter
// 委托读写，Vue 响应式通过委托传播。
export const themeStore = reactive({
  theme: "dark",
  accentTheme: "blue" as AccentTheme,
  customAccent: null as CustomAccentTheme | null,
  designLanguage: "apple" as "apple" | "claude",
  fontSizeScaling: 100,
  uiDensity: "comfortable",
});

const EDITOR_EXPERIENCE_STORAGE_KEY = "koyori-ide.editorExperience.v1";

interface PersistedEditorExperience {
  stickyScrollEnabled?: boolean;
  inlayHintsEnabled?: boolean;
  organizeImportsOnSave?: boolean;
}

function readPersistedEditorExperience(): PersistedEditorExperience {
  try {
    const raw = globalThis.localStorage?.getItem(EDITOR_EXPERIENCE_STORAGE_KEY);
    if (!raw) return {};
    const value = JSON.parse(raw) as unknown;
    return value && typeof value === "object"
      ? value as PersistedEditorExperience
      : {};
  } catch {
    return {};
  }
}

const persistedEditorExperience = readPersistedEditorExperience();

export const settingsStore = reactive({
  fontSize: 14,
  fontFamily: "JetBrains Mono",
  tabSize: 2,
  wordWrap: true,
  minimap: true,
  stickyScrollEnabled: persistedEditorExperience.stickyScrollEnabled !== false,
  inlayHintsEnabled: persistedEditorExperience.inlayHintsEnabled !== false,
  organizeImportsOnSave: persistedEditorExperience.organizeImportsOnSave !== false,
  lineNumbers: true,
  cursorBlinking: "smooth",
  cursorStyle: "line",
  bracketColorization: true,
  autoSave: false,
  autoSaveDelay: "afterDelay",
  formatOnSave: true,
  trimTrailingWhitespace: false,
  insertSpaces: true,
  insertFinalNewline: true,
  gitBlameEnabled: false,
  emmetEnabled: true,
  emmetIncludeLanguages: {},
  language: "en",
  autoUpdate: true,
  dataFolderPath: "",
  enablePluginSandbox: true,
  customShortcuts: {} as Record<string, ShortcutKeys>,
  defaultShell: "",
  terminalFontSize: 13,
  terminalCursorStyle: "block",
  scrollback: 10000,
  settingsVersion: 0,
});

export const aiConfigStore = reactive({
  aiApiKey: "",
  aiApiKeyConfigured: false,
  aiApiKeyStorageMethod: "none",
  aiBaseUrl: "https://api.openai.com",
  aiModel: "gpt-4o",
  aiSystemPrompt: "",
  aiAgentSystemPrompt: "",
  aiConversationTitlePrompt: "",
  aiProvider: "",
  temperature: 0.7,
  reasoningEffort: "" as ReasoningEffort,
  maxTokens: 4096,
  aiChatPosition: "right" as "left" | "right",
  aiProviderConfigs: [] as AIProviderConfig[],
  activeAIConfigId: "",
  agentPermissionMode: "always-ask" as AgentPermissionMode,
  inlineCompletionEnabled: true,
  aiWindowTheme: "apple-dark" as AIWindowTheme,
  aiSidebarWidth: 288,
  aiTerminalWidth: 440,
  openAIWindowOnStartup: false,
});

export const projectStore = reactive({
  currentProject: null as string | null,
  projectName: null as string | null,
  workspaceRoot: null as string | null,
  currentFilePath: null as string | null,
  workspaceFolders: [] as string[],
  workspaceGeneration: 0,
  branchName: "main",
  errors: 0,
  warnings: 0,
  cursorLine: 1,
  cursorColumn: 1,
  editorJumpSeq: 0,
  editorJumpTargetPath: null as string | null,
  editorJumpTargetGroupId: null as string | null,
  editorJumpTargetSeq: 0,
  encoding: "UTF-8",
  languageMode: "TypeScript",
  bottomPanelView: "" as "terminal" | "output" | "problems" | "debug" | "tasks" | "workflows" | "profile" | "",
  toolPaths: {} as Record<string, string>,
  personalization: { codeEditorBgOpacity: 0, codeEditorBgBlur: 0, chatBgOpacity: 0, chatBgBlur: 0, bubbleStyle: "rounded", bubbleOpacity: 1, messageSpacing: 12 } as PersonalizationConfig,
  editorGroupFilePaths: {} as Record<string, string[]>,
  editorGroupActiveFiles: {} as Record<string, string | null>,
});

export const windowStore = reactive({
  sidebarCollapsed: false,
  sidebarWidth: 260,
  activityBarWidth: 48,
  panelTab: "explorer" as PanelTab,
  extensionsSubview: "installed" as ExtensionsSubview,
  activeExtensionView: null as string | null,
  terminalVisible: true,
  terminalHeight: 220,
  aiChatVisible: false,
  aiChatWidth: 380,
  statusBarVisible: true,
  breadcrumbVisible: true,
  activityBarVisible: true,
  isWindowMaximised: false,
});

// 紧凑字段→store 映射，构建 _appBase 后包装为 reactive。
const _storeFields = {
  theme: ["theme", "accentTheme", "customAccent", "designLanguage", "fontSizeScaling", "uiDensity"],
  settings: ["fontSize", "fontFamily", "tabSize", "wordWrap", "minimap", "stickyScrollEnabled", "inlayHintsEnabled", "organizeImportsOnSave", "lineNumbers", "cursorBlinking", "cursorStyle", "bracketColorization", "autoSave", "autoSaveDelay", "formatOnSave", "trimTrailingWhitespace", "insertSpaces", "insertFinalNewline", "gitBlameEnabled", "emmetEnabled", "emmetIncludeLanguages", "language", "autoUpdate", "dataFolderPath", "enablePluginSandbox", "customShortcuts", "defaultShell", "terminalFontSize", "terminalCursorStyle", "scrollback", "settingsVersion"],
  aiConfig: ["aiApiKey", "aiApiKeyConfigured", "aiApiKeyStorageMethod", "aiBaseUrl", "aiModel", "aiSystemPrompt", "aiAgentSystemPrompt", "aiConversationTitlePrompt", "aiInlineCompletionPrompt", "aiProvider", "temperature", "reasoningEffort", "maxTokens", "aiChatPosition", "aiProviderConfigs", "activeAIConfigId", "agentPermissionMode", "inlineCompletionEnabled", "aiWindowTheme", "aiSidebarWidth", "aiTerminalWidth", "openAIWindowOnStartup"],
  project: ["currentProject", "projectName", "workspaceRoot", "currentFilePath", "workspaceFolders", "workspaceGeneration", "branchName", "errors", "warnings", "cursorLine", "cursorColumn", "editorJumpSeq", "editorJumpTargetPath", "editorJumpTargetGroupId", "editorJumpTargetSeq", "encoding", "languageMode", "bottomPanelView", "toolPaths", "personalization", "editorGroupFilePaths", "editorGroupActiveFiles"],
  window: ["sidebarCollapsed", "sidebarWidth", "activityBarWidth", "panelTab", "extensionsSubview", "activeExtensionView", "terminalVisible", "terminalHeight", "aiChatVisible", "aiChatWidth", "statusBarVisible", "breadcrumbVisible", "activityBarVisible", "isWindowMaximised"],
} as const;

const _subStores = { theme: themeStore, settings: settingsStore, aiConfig: aiConfigStore, project: projectStore, window: windowStore } as const;

const _appBase: Record<string, unknown> = {};
for (const [storeKey, fields] of Object.entries(_storeFields)) {
  for (const field of fields) {
    Object.defineProperty(_appBase, field, {
      get: () => (_subStores[storeKey as keyof typeof _subStores] as Record<string, unknown>)[field],
      set: (v: unknown) => { (_subStores[storeKey as keyof typeof _subStores] as Record<string, unknown>)[field] = v; },
      enumerable: true,
      configurable: true,
    });
  }
}
Object.defineProperty(_appBase, "minimapEnabled", {
  get: () => settingsStore.minimap,
  set: (value: unknown) => { settingsStore.minimap = Boolean(value); },
  enumerable: true,
  configurable: true,
});
export const appState = reactive(_appBase) as unknown as AppState;

/** Queue a caret jump for one editor file/group without waking every split. */
export function requestEditorJump(
  path: string,
  line: number,
  column = 1,
  groupId: string | null = null,
): number {
  const nextSeq = (appState.editorJumpSeq || 0) + 1;
  appState.cursorLine = Number.isFinite(line) ? Math.max(1, Math.trunc(line)) : 1;
  appState.cursorColumn = Number.isFinite(column) ? Math.max(1, Math.trunc(column)) : 1;
  appState.editorJumpTargetPath = path || null;
  appState.editorJumpTargetGroupId = groupId || null;
  appState.editorJumpTargetSeq = nextSeq;
  appState.editorJumpSeq = nextSeq;
  return nextSeq;
}

let editorExperienceSaveTimer: ReturnType<typeof setTimeout> | null = null;
let stopEditorExperienceWatch: WatchStopHandle | null = null;

/** Start or replace module-level appState persistence effects. */
export function initAppStateEffects(): void {
  stopEditorExperienceWatch?.();
  stopEditorExperienceWatch = watch(
    () => [
      appState.stickyScrollEnabled,
      appState.inlayHintsEnabled,
      appState.organizeImportsOnSave,
    ] as const,
    ([stickyScrollEnabled, inlayHintsEnabled, organizeImportsOnSave]) => {
      if (editorExperienceSaveTimer) clearTimeout(editorExperienceSaveTimer);
      editorExperienceSaveTimer = setTimeout(() => {
        editorExperienceSaveTimer = null;
        try {
          globalThis.localStorage?.setItem(EDITOR_EXPERIENCE_STORAGE_KEY, JSON.stringify({
            stickyScrollEnabled,
            inlayHintsEnabled,
            organizeImportsOnSave,
          } satisfies PersistedEditorExperience));
        } catch {
          // Settings remain live for the current session when storage is unavailable.
        }
      }, 500);
    },
  );
}

/** Release module-level appState effects during HMR teardown. */
export function teardownAppStateEffects(): void {
  stopEditorExperienceWatch?.();
  stopEditorExperienceWatch = null;
  if (editorExperienceSaveTimer) {
    clearTimeout(editorExperienceSaveTimer);
    editorExperienceSaveTimer = null;
  }
}

function uniquePaths(paths: readonly string[]): string[] {
  return [...new Set(paths.filter(Boolean))];
}

export function ensureEditorGroup(
  groupId: string,
  fallbackPaths?: readonly string[],
  preferredActivePath: string | null = null,
): void {
  if (!Object.prototype.hasOwnProperty.call(appState.editorGroupFilePaths, groupId)) {
    appState.editorGroupFilePaths[groupId] = uniquePaths(fallbackPaths ?? []);
  }
  let paths = appState.editorGroupFilePaths[groupId];
  if (fallbackPaths) {
    const openPaths = new Set(fallbackPaths);
    const retainedPaths = paths.filter((path) => openPaths.has(path));
    if (retainedPaths.length !== paths.length) {
      appState.editorGroupFilePaths[groupId] = retainedPaths;
      paths = retainedPaths;
    }
  }
  if (
    paths.length === 0
    && (fallbackPaths?.length ?? 0) > 0
    && Object.keys(appState.editorGroupFilePaths).length <= 1
  ) {
    appState.editorGroupFilePaths[groupId] = uniquePaths(fallbackPaths ?? []);
    paths = appState.editorGroupFilePaths[groupId];
  }
  const current = appState.editorGroupActiveFiles[groupId];
  if (current && paths.includes(current)) return;
  appState.editorGroupActiveFiles[groupId] = preferredActivePath && paths.includes(preferredActivePath)
    ? preferredActivePath
    : paths[0] ?? null;
}

export function addFileToEditorGroup(
  groupId: string,
  path: string,
  activate = true,
): void {
  ensureEditorGroup(groupId);
  const paths = appState.editorGroupFilePaths[groupId];
  if (!paths.includes(path)) paths.push(path);
  if (activate) appState.editorGroupActiveFiles[groupId] = path;
}

export function activateEditorGroupFile(groupId: string, path: string): void {
  addFileToEditorGroup(groupId, path, true);
}

export function removeFileFromEditorGroup(groupId: string, path: string): void {
  ensureEditorGroup(groupId);
  const paths = appState.editorGroupFilePaths[groupId];
  const index = paths.indexOf(path);
  if (index < 0) return;
  paths.splice(index, 1);
  if (appState.editorGroupActiveFiles[groupId] === path) {
    appState.editorGroupActiveFiles[groupId] = paths[Math.min(index, paths.length - 1)] ?? null;
  }
}

export function removeFileFromAllEditorGroups(path: string): void {
  for (const groupId of Object.keys(appState.editorGroupFilePaths)) {
    removeFileFromEditorGroup(groupId, path);
  }
}

export function editorGroupsContainingFile(path: string): string[] {
  return Object.entries(appState.editorGroupFilePaths)
    .filter(([, paths]) => paths.includes(path))
    .map(([groupId]) => groupId);
}

export function moveFileBetweenEditorGroups(
  path: string,
  sourceGroupId: string,
  targetGroupId: string,
  targetIndex?: number,
): void {
  ensureEditorGroup(sourceGroupId);
  ensureEditorGroup(targetGroupId);
  const sourcePaths = appState.editorGroupFilePaths[sourceGroupId];
  const sourceIndex = sourcePaths.indexOf(path);
  if (sourceIndex >= 0) sourcePaths.splice(sourceIndex, 1);
  if (appState.editorGroupActiveFiles[sourceGroupId] === path) {
    appState.editorGroupActiveFiles[sourceGroupId] = sourcePaths[Math.min(sourceIndex, sourcePaths.length - 1)] ?? null;
  }

  const targetPaths = appState.editorGroupFilePaths[targetGroupId];
  const existingIndex = targetPaths.indexOf(path);
  if (existingIndex >= 0) targetPaths.splice(existingIndex, 1);
  const insertionIndex = targetIndex === undefined
    ? targetPaths.length
    : Math.max(0, Math.min(targetIndex, targetPaths.length));
  targetPaths.splice(insertionIndex, 0, path);
  appState.editorGroupActiveFiles[targetGroupId] = path;
}

export function cloneEditorGroup(
  sourceGroupId: string,
  targetGroupId: string,
  activeFileOnly = false,
): void {
  ensureEditorGroup(sourceGroupId);
  const activePath = appState.editorGroupActiveFiles[sourceGroupId] ?? null;
  const paths = activeFileOnly && activePath
    ? [activePath]
    : [...appState.editorGroupFilePaths[sourceGroupId]];
  appState.editorGroupFilePaths[targetGroupId] = paths;
  appState.editorGroupActiveFiles[targetGroupId] = activePath && paths.includes(activePath)
    ? activePath
    : paths[0] ?? null;
}

export function removeEditorGroup(groupId: string): void {
  delete appState.editorGroupFilePaths[groupId];
  delete appState.editorGroupActiveFiles[groupId];
}

export const isEditorReady = computed(() => appState.currentProject !== null);
export const currentAccentMeta = computed(() => accentThemes[appState.accentTheme]);

// 模块级只初始化窗口内 watch；跨窗口监听统一由 crossWindowSync 编排。
initAppActions();
initAppStateEffects();

import.meta.hot?.dispose(() => {
  unregisterAppListeners();
  teardownAppStateEffects();
});
