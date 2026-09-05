/**
 * F-10 (task-5.md): App actions（从 app.ts 拆出的行为层）.
 *
 * 本模块承接 app.ts 中所有非聚合函数：
 *   - Settings 加载/保存（loadSettings / saveSettings / migrateLegacyAIConfig）
 *   - 生命周期监听器（init/cleanup/start/stop* + unregisterAppListeners）
 *   - Theme 应用（applyAccentTheme / applyMode / applyDesignLanguage /
 *     applyFontSizeScaling / applyUiDensity / initThemes / resolveSystemMode）
 *   - 自定义 accent（setCustomAccent）
 *   - AI Config 管理（activateAIConfig / saveAIConfig / deleteAIConfig /
 *     createNewAIConfig / activeAIConfig / generateAIConfigId）
 *   - 面板切换（setPanelTab / setActiveExtensionView / setExtensionsSubview）
 *   - watch 块（theme / currentProject 触发的副作用）
 *
 * 兼容性：app.ts re-export 本模块的所有公开符号，旧代码
 * `import { loadSettings, applyMode, ... } from "@/stores/app"` 继续可用。
 *
 * 循环依赖说明：本模块从 app.ts 导入 appState；app.ts re-export 本模块函数。
 * ES module 循环导入在函数体内使用对方导出是安全的。watch 块通过
 * initAppActions() 延迟执行，避免模块顶层读取 appState。
 */
// Koyori IDE 模块 · App Actions；交互服务：设置（SettingsService）、窗口（WindowService）。
// 喵，这是 Koyori IDE 的 App Actions 模块（前端实现）~
import { reactive, watch, type WatchStopHandle } from "vue";
import { Events } from "@wailsio/runtime";
import { settingsService, windowService } from "@/api/services";
import type {
  Settings,
  CustomAccentTheme,
  AIProviderConfig,
} from "@/types";
import type { AccentTheme } from "@/lib/monaco-themes";
import {
  applyCustomAccentTokens,
  clearCustomAccentTokens,
} from "@/stores/themeEditor";
import {
  applyMonacoTheme,
  applyMonacoThemeForMode,
  clearVscodeExtensionTheme,
  registerAllThemes,
  registerCustomTheme,
} from "@/lib/monaco-themes";
import { PROVIDER_PRESETS } from "@/lib/aiProviders";
import { loadRules, clearRules } from "@/stores/rules";
import { translate } from "@/lib/i18n";
import { notifyError, notifyWarning } from "@/lib/notifications";
import { loadCustomShortcuts, getCustomShortcuts } from "@/composables/useKeyboard";
import {
  getWindowOriginId,
  unwrapEventData,
  parseSyncOrigin,
} from "@/lib/windowOrigin";
import { appState } from "@/stores/app";
import type { PanelTab, ExtensionsSubview, ThemeMode } from "@/stores/app";

// ---------------------------------------------------------------------------
// appInternals — 模块级生命周期变量（非应用状态）
// ---------------------------------------------------------------------------

const appInternals = reactive({
  systemModeCleanup: null as (() => void) | null,
  windowMaximiseListenerInitialised: false,
  saveTimer: null as ReturnType<typeof setTimeout> | null,
  settingsSyncListenerRegistered: false,
  applyingRemoteSettings: false,
  projectRemovedListenerRegistered: false,
});
let appActionWatchStops: WatchStopHandle[] = [];
let appActionGeneration = 0;
let windowListenerGeneration = 0;
let settingsListenerGeneration = 0;
let projectListenerGeneration = 0;

function stopAppActionWatchers(): void {
  for (const stop of appActionWatchStops) stop();
  appActionWatchStops = [];
}

// ---------------------------------------------------------------------------
// Theme 应用函数
// ---------------------------------------------------------------------------

export function resolveSystemMode(): "dark" | "light" {
  if (typeof window !== "undefined" && window.matchMedia) {
    return window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
  }
  return "dark";
}

export function applyAccentTheme(accent: AccentTheme): void {
  clearVscodeExtensionTheme();
  appState.accentTheme = accent;
  if (accent === "custom" && appState.customAccent) {
    applyCustomAccentTokens(appState.customAccent);
    registerCustomTheme(appState.customAccent.color);
  } else {
    clearCustomAccentTokens();
    document.documentElement.setAttribute("data-theme", accent);
  }
  applyMonacoTheme(accent);
}

export function applyMode(mode: ThemeMode): void {
  appState.theme = mode;
  const resolved: "dark" | "light" = mode === "system" ? resolveSystemMode() : mode;
  document.documentElement.setAttribute("data-mode", resolved);
  if (appState.accentTheme === "custom" && appState.customAccent) {
    registerCustomTheme(appState.customAccent.color);
  }
  applyMonacoThemeForMode(appState.accentTheme, resolved);
}

export function applyDesignLanguage(lang: "apple" | "claude"): void {
  appState.designLanguage = lang;
  if (lang === "apple") {
    document.documentElement.removeAttribute("data-design-language");
  } else {
    document.documentElement.setAttribute("data-design-language", lang);
  }
}

export function applyFontSizeScaling(scale: number): void {
  appState.fontSizeScaling = scale;
  const clamped = Math.max(80, Math.min(150, Math.round(scale)));
  document.documentElement.style.setProperty("--font-scale", (clamped / 100).toFixed(2));
}

export function applyUiDensity(density: string): void {
  appState.uiDensity = density;
  const valid = density === "compact" || density === "comfortable" || density === "spacious"
    ? density
    : "comfortable";
  document.documentElement.setAttribute("data-density", valid);
}

export function initThemes(): void {
  registerAllThemes();
  if (appState.accentTheme === "custom" && appState.customAccent) {
    applyCustomAccentTokens(appState.customAccent);
    registerCustomTheme(appState.customAccent.color);
  } else {
    document.documentElement.setAttribute("data-theme", appState.accentTheme);
  }
  const resolved: "dark" | "light" = appState.theme === "system" ? resolveSystemMode() : (appState.theme as "dark" | "light");
  document.documentElement.setAttribute("data-mode", resolved);
  applyMonacoThemeForMode(appState.accentTheme, resolved);
  applyDesignLanguage(appState.designLanguage);
  applyFontSizeScaling(appState.fontSizeScaling);
  applyUiDensity(appState.uiDensity);
}

export function setCustomAccent(custom: CustomAccentTheme): void {
  clearVscodeExtensionTheme();
  appState.customAccent = custom;
  appState.accentTheme = "custom";
  applyCustomAccentTokens(custom);
  registerCustomTheme(custom.color);
  applyMonacoTheme("custom");
  saveSettings();
}

// ---------------------------------------------------------------------------
// System mode listener
// ---------------------------------------------------------------------------

export function startSystemModeListener(): void {
  if (typeof window === "undefined" || !window.matchMedia) return;
  if (appInternals.systemModeCleanup) return;
  const mq = window.matchMedia("(prefers-color-scheme: light)");
  const handler = () => {
    if (appState.theme === "system") {
      const resolved = resolveSystemMode();
      document.documentElement.setAttribute("data-mode", resolved);
      if (appState.accentTheme === "custom" && appState.customAccent) {
        registerCustomTheme(appState.customAccent.color);
      }
      applyMonacoThemeForMode(appState.accentTheme, resolved);
    }
  };
  mq.addEventListener("change", handler);
  appInternals.systemModeCleanup = () => mq.removeEventListener("change", handler);
}

export function stopSystemModeListener(): void {
  if (appInternals.systemModeCleanup) {
    appInternals.systemModeCleanup();
    appInternals.systemModeCleanup = null;
  }
}

// ---------------------------------------------------------------------------
// Window maximise listener
// ---------------------------------------------------------------------------

export function initWindowMaximiseListener(): void {
  if (appInternals.windowMaximiseListenerInitialised) return;
  appInternals.windowMaximiseListenerInitialised = true;
  const generation = ++windowListenerGeneration;
  void windowService.isMaximised().then((max) => {
    if (generation !== windowListenerGeneration) return;
    appState.isWindowMaximised = !!max;
  }).catch(() => {
    /* backend not ready; event listener will take over */
  });
}

export function handleWindowMaximisedEvent(event: unknown): void {
  if (!appInternals.windowMaximiseListenerInitialised) return;
  const data = unwrapEventData(event);
  appState.isWindowMaximised = data === true;
}

export function stopWindowMaximiseListener(): void {
  windowListenerGeneration += 1;
  appInternals.windowMaximiseListenerInitialised = false;
}

// ---------------------------------------------------------------------------
// Settings load / save
// ---------------------------------------------------------------------------

function migrateLegacyAIConfig(settings: Settings): AIProviderConfig {
  const provider = settings.aiProvider || "openai";
  const preset = PROVIDER_PRESETS.find((p) => p.id === provider);
  return {
    id: generateAIConfigId(),
    name: preset?.label ?? "Default",
    provider,
    protocol: preset?.protocol ?? "openai",
    apiKey: settings.aiApiKey ?? "",
    baseUrl: settings.aiBaseUrl ?? "",
    model: settings.aiModel ?? "",
    temperature: settings.temperature ?? 0.7,
    maxTokens: settings.maxTokens ?? 4096,
  };
}

export async function loadSettings(
  shouldApply: () => boolean = () => true,
): Promise<void> {
  try {
    const settings = await settingsService.loadSettings();
    if (!shouldApply()) return;
    appState.settingsVersion = settings.version ?? 0;
    appState.language = settings.language;
    appState.theme = settings.theme;
    appState.fontSize = settings.fontSize;
    appState.fontFamily = settings.fontFamily;
    appState.tabSize = settings.tabSize;
    appState.wordWrap = settings.wordWrap;
    appState.lineNumbers = settings.lineNumbers;
    appState.minimap = settings.minimap;
    appState.aiApiKey = "";
    appState.aiApiKeyConfigured = settings.aiApiKeyConfigured ?? false;
    appState.aiApiKeyStorageMethod = settings.aiApiKeyStorageMethod ?? "none";
    appState.aiBaseUrl = settings.aiBaseUrl;
    appState.aiModel = settings.aiModel;
    appState.aiSystemPrompt = settings.aiSystemPrompt;
    appState.aiAgentSystemPrompt = settings.aiAgentSystemPrompt ?? "";
    appState.aiConversationTitlePrompt = settings.aiConversationTitlePrompt ?? "";
    appState.aiInlineCompletionPrompt = settings.aiInlineCompletionPrompt ?? "";
    appState.cursorBlinking = settings.cursorBlinking;
    appState.cursorStyle = settings.cursorStyle;
    appState.bracketColorization = settings.bracketColorization;
    appState.autoSave = settings.autoSave;
    appState.autoSaveDelay = settings.autoSaveDelay;
    appState.aiProvider = settings.aiProvider;
    appState.temperature = settings.temperature;
    appState.maxTokens = settings.maxTokens;
    appState.defaultShell = settings.defaultShell;
    appState.terminalFontSize = settings.terminalFontSize;
    appState.terminalCursorStyle = settings.terminalCursorStyle;
    appState.scrollback = settings.scrollback;
    appState.uiDensity = settings.uiDensity;
    appState.fontSizeScaling = settings.fontSizeScaling;
    appState.inlineCompletionEnabled = settings.inlineCompletionEnabled;
    appState.formatOnSave = settings.formatOnSave !== false;
    appState.trimTrailingWhitespace = settings.trimTrailingWhitespace === true;
    appState.insertSpaces = settings.insertSpaces !== false;
    appState.insertFinalNewline = settings.insertFinalNewline !== false;
    appState.gitBlameEnabled = settings.gitBlameEnabled === true;
    appState.emmetEnabled = settings.emmetEnabled !== false;
    appState.emmetIncludeLanguages = { ...(settings.emmetIncludeLanguages ?? {}) };
    const custom = settings.customShortcuts ?? {};
    loadCustomShortcuts(custom);
    appState.customShortcuts = { ...custom };
    appState.aiChatPosition = settings.aiChatPosition === "left" ? "left" : "right";
    appState.activityBarVisible = settings.activityBarVisible !== false;
    appState.agentPermissionMode = settings.agentPermissionMode ?? "always-ask";
    if (settings.accentTheme) {
      appState.accentTheme = settings.accentTheme as AccentTheme;
    }
    appState.customAccent = settings.customAccent ?? null;
    appState.enablePluginSandbox = settings.enablePluginSandbox !== false;
    if (settings.designLanguage === "claude" || settings.designLanguage === "apple") {
      appState.designLanguage = settings.designLanguage;
    }
    const loadedConfigs = settings.aiProviderConfigs ?? [];
    if (loadedConfigs.length > 0) {
      appState.aiProviderConfigs = loadedConfigs;
      appState.activeAIConfigId = settings.activeAIConfigId ?? loadedConfigs[0].id;
    } else {
      const migrated = migrateLegacyAIConfig(settings);
      appState.aiProviderConfigs = [migrated];
      appState.activeAIConfigId = migrated.id;
    }
    const activeConfig = appState.aiProviderConfigs.find((cfg) => cfg.id === appState.activeAIConfigId);
    appState.reasoningEffort = activeConfig?.reasoningEffort ?? "";
    appState.aiApiKey = "";
    appState.toolPaths = { ...(settings.toolPaths ?? {}) };
    if (settings.personalization) {
      appState.personalization = { ...appState.personalization, ...settings.personalization };
    }
    appState.openAIWindowOnStartup = settings.openAIWindowOnStartup === true;
    appState.aiWindowTheme = settings.aiWindowTheme ?? "apple-dark";
    appState.aiSidebarWidth = settings.aiSidebarWidth ?? 288;
    appState.aiTerminalWidth = settings.aiTerminalWidth ?? 440;
    try {
      const { syncAIWindowPreferences } = await import("@/stores/aiWindow");
      if (!shouldApply()) return;
      syncAIWindowPreferences({
        theme: appState.aiWindowTheme,
        sidebarWidth: appState.aiSidebarWidth,
        terminalWidth: appState.aiTerminalWidth,
      });
    } catch {
      /* AI-window UI store may be unavailable in isolated unit tests */
    }
  } catch (e) {
    if (!shouldApply()) return;
    console.error("Failed to load settings:", e);
    notifyError(translate("settings.loadFailed", { error: e instanceof Error ? e.message : String(e) }));
  }
}

export function saveSettings(): void {
  if (appInternals.saveTimer) clearTimeout(appInternals.saveTimer);
  const generation = appActionGeneration;
  appInternals.saveTimer = setTimeout(async () => {
    appInternals.saveTimer = null;
    const settings: Settings = {
      schemaVersion: 1,
      expectedVersion: appState.settingsVersion > 0 ? appState.settingsVersion : undefined,
      version: appState.settingsVersion,
      language: appState.language,
      theme: appState.theme,
      fontSize: appState.fontSize,
      fontFamily: appState.fontFamily,
      tabSize: appState.tabSize,
      wordWrap: appState.wordWrap,
      lineNumbers: appState.lineNumbers,
      minimap: appState.minimap,
      aiApiKey: appState.aiApiKey,
      aiApiKeyConfigured: appState.aiApiKeyConfigured,
      aiApiKeyStorageMethod: appState.aiApiKeyStorageMethod,
      aiBaseUrl: appState.aiBaseUrl,
      aiModel: appState.aiModel,
      aiSystemPrompt: appState.aiSystemPrompt,
      aiAgentSystemPrompt: appState.aiAgentSystemPrompt,
      aiConversationTitlePrompt: appState.aiConversationTitlePrompt,
      aiInlineCompletionPrompt: appState.aiInlineCompletionPrompt,
      cursorBlinking: appState.cursorBlinking,
      cursorStyle: appState.cursorStyle,
      bracketColorization: appState.bracketColorization,
      autoSave: appState.autoSave,
      autoSaveDelay: appState.autoSaveDelay,
      aiProvider: appState.aiProvider,
      temperature: appState.temperature,
      maxTokens: appState.maxTokens,
      defaultShell: appState.defaultShell,
      terminalFontSize: appState.terminalFontSize,
      terminalCursorStyle: appState.terminalCursorStyle,
      scrollback: appState.scrollback,
      uiDensity: appState.uiDensity,
      fontSizeScaling: appState.fontSizeScaling,
      inlineCompletionEnabled: appState.inlineCompletionEnabled,
      formatOnSave: appState.formatOnSave,
      trimTrailingWhitespace: appState.trimTrailingWhitespace,
      insertSpaces: appState.insertSpaces,
      insertFinalNewline: appState.insertFinalNewline,
      gitBlameEnabled: appState.gitBlameEnabled,
      emmetEnabled: appState.emmetEnabled,
      emmetIncludeLanguages: { ...appState.emmetIncludeLanguages },
      customShortcuts: getCustomShortcuts(),
      aiChatPosition: appState.aiChatPosition,
      activityBarVisible: appState.activityBarVisible,
      agentPermissionMode: appState.agentPermissionMode,
      accentTheme: appState.accentTheme,
      customAccent: appState.customAccent,
      enablePluginSandbox: appState.enablePluginSandbox,
      designLanguage: appState.designLanguage,
      aiProviderConfigs: appState.aiProviderConfigs,
      activeAIConfigId: appState.activeAIConfigId,
      toolPaths: { ...appState.toolPaths },
      personalization: { ...appState.personalization },
      openAIWindowOnStartup: appState.openAIWindowOnStartup,
      aiWindowTheme: appState.aiWindowTheme,
      aiSidebarWidth: appState.aiSidebarWidth,
      aiTerminalWidth: appState.aiTerminalWidth,
    };
    try {
      await settingsService.saveSettings(settings);
      if (generation !== appActionGeneration) return;
      appState.settingsVersion =
        appState.settingsVersion > 0 ? appState.settingsVersion + 1 : 1;
      try {
        void Events.Emit("settings:changed", {
          origin: getWindowOriginId(),
          version: appState.settingsVersion,
          at: Date.now(),
        });
      } catch {
        /* Events may be unavailable in unit tests */
      }
    } catch (e) {
      if (generation !== appActionGeneration) return;
      console.error("Failed to save settings:", e);
      const msg = e instanceof Error ? e.message : String(e);
      if (/settings version conflict|version conflict/i.test(msg)) {
        notifyError(
          translate("settings.versionConflict"),
          translate("settings.versionConflictTitle"),
        );
        void loadSettings();
      } else {
        notifyError(translate("settings.saveFailed", { error: msg }));
      }
    }
  }, 500);
}

// P9-G11: flush a pending debounced settings save immediately (used on
// window close so debounce-pending changes are not lost).
export async function flushSettingsSave(): Promise<void> {
  if (!appInternals.saveTimer) return;
  clearTimeout(appInternals.saveTimer);
  appInternals.saveTimer = null;
  const generation = appActionGeneration;
  const settings: Settings = {
    schemaVersion: 1,
    expectedVersion: appState.settingsVersion > 0 ? appState.settingsVersion : undefined,
    version: appState.settingsVersion,
    language: appState.language,
    theme: appState.theme,
    fontSize: appState.fontSize,
    fontFamily: appState.fontFamily,
    tabSize: appState.tabSize,
    wordWrap: appState.wordWrap,
    lineNumbers: appState.lineNumbers,
    minimap: appState.minimap,
    aiApiKey: appState.aiApiKey,
    aiApiKeyConfigured: appState.aiApiKeyConfigured,
    aiApiKeyStorageMethod: appState.aiApiKeyStorageMethod,
    aiBaseUrl: appState.aiBaseUrl,
    aiModel: appState.aiModel,
    aiSystemPrompt: appState.aiSystemPrompt,
    aiProvider: appState.aiProvider,
    aiAgentSystemPrompt: appState.aiAgentSystemPrompt,
    aiConversationTitlePrompt: appState.aiConversationTitlePrompt,
    aiInlineCompletionPrompt: appState.aiInlineCompletionPrompt,
    cursorBlinking: appState.cursorBlinking,
    cursorStyle: appState.cursorStyle,
    bracketColorization: appState.bracketColorization,
    autoSave: appState.autoSave,
    autoSaveDelay: appState.autoSaveDelay,
    temperature: appState.temperature,
    maxTokens: appState.maxTokens,
    defaultShell: appState.defaultShell,
    terminalFontSize: appState.terminalFontSize,
    terminalCursorStyle: appState.terminalCursorStyle,
    scrollback: appState.scrollback,
    uiDensity: appState.uiDensity,
    fontSizeScaling: appState.fontSizeScaling,
    inlineCompletionEnabled: appState.inlineCompletionEnabled,
    formatOnSave: appState.formatOnSave,
    trimTrailingWhitespace: appState.trimTrailingWhitespace,
    insertSpaces: appState.insertSpaces,
    insertFinalNewline: appState.insertFinalNewline,
    gitBlameEnabled: appState.gitBlameEnabled,
    emmetEnabled: appState.emmetEnabled,
    emmetIncludeLanguages: { ...appState.emmetIncludeLanguages },
    customShortcuts: getCustomShortcuts(),
    aiChatPosition: appState.aiChatPosition,
    activityBarVisible: appState.activityBarVisible,
    agentPermissionMode: appState.agentPermissionMode,
    accentTheme: appState.accentTheme,
    customAccent: appState.customAccent,
    enablePluginSandbox: appState.enablePluginSandbox,
    designLanguage: appState.designLanguage,
    aiProviderConfigs: appState.aiProviderConfigs,
    activeAIConfigId: appState.activeAIConfigId,
    toolPaths: { ...appState.toolPaths },
    personalization: { ...appState.personalization },
    openAIWindowOnStartup: appState.openAIWindowOnStartup,
    aiWindowTheme: appState.aiWindowTheme,
    aiSidebarWidth: appState.aiSidebarWidth,
    aiTerminalWidth: appState.aiTerminalWidth,
  };
  try {
    await settingsService.saveSettings(settings);
    if (generation !== appActionGeneration) return;
    appState.settingsVersion =
      appState.settingsVersion > 0 ? appState.settingsVersion + 1 : 1;
    try {
      void Events.Emit("settings:changed", {
        origin: getWindowOriginId(),
        version: appState.settingsVersion,
        at: Date.now(),
      });
    } catch {
      /* Events may be unavailable in unit tests */
    }
  } catch (e) {
    if (generation !== appActionGeneration) return;
    console.error("Failed to save settings:", e);
    const msg = e instanceof Error ? e.message : String(e);
    if (/settings version conflict|version conflict/i.test(msg)) {
      notifyError(
        translate("settings.versionConflict"),
        translate("settings.versionConflictTitle"),
      );
      void loadSettings();
    } else {
      notifyError(translate("settings.saveFailed", { error: msg }));
    }
  }
}

// ---------------------------------------------------------------------------
// Settings sync listener (peer webviews)
// ---------------------------------------------------------------------------

export function initSettingsSyncListener(): void {
  settingsListenerGeneration += 1;
  appInternals.settingsSyncListenerRegistered = true;
}

export function handleSettingsChangedEvent(event: unknown): void {
  if (!appInternals.settingsSyncListenerRegistered) return;
  const generation = settingsListenerGeneration;
  const payload = unwrapEventData(event);
  const origin = parseSyncOrigin(payload);
  if (origin && origin === getWindowOriginId()) return;
  if (appInternals.applyingRemoteSettings) return;
  appInternals.applyingRemoteSettings = true;
  void loadSettings(() => generation === settingsListenerGeneration)
    .catch(() => { /* best-effort */ })
    .finally(() => {
      if (generation === settingsListenerGeneration) {
        appInternals.applyingRemoteSettings = false;
      }
    });
}

export function cleanupSettingsSyncListener(): void {
  settingsListenerGeneration += 1;
  appInternals.applyingRemoteSettings = false;
  appInternals.settingsSyncListenerRegistered = false;
}

// ---------------------------------------------------------------------------
// Project removed listener
// ---------------------------------------------------------------------------

export function initProjectRemovedListener(): void {
  projectListenerGeneration += 1;
  appInternals.projectRemovedListenerRegistered = true;
}

export function handleProjectRemovedEvent(event: unknown): void {
  if (!appInternals.projectRemovedListenerRegistered) return;
  const generation = projectListenerGeneration;
  const data = unwrapEventData(event) as { id?: string; path?: string; name?: string } | null;
  if (!data?.path) return;
  const removedPath = data.path;
  // `project:removed` is a cleanup hint only. Workspace identity (including
  // root, roots, and generation) is owned by ProjectService; re-read its
  // committed snapshot instead of mutating a renderer-local partial state.
  void import("@/stores/workspaceStore").then(({ syncWorkspaceSnapshot }) => {
    if (generation !== projectListenerGeneration) return;
    void syncWorkspaceSnapshot().catch((error) => {
      console.warn("[workspace] refresh after project removal failed", error);
    });
  });
  void import("@/stores/editor").then(({ editorState, closeFile }) => {
    if (generation !== projectListenerGeneration) return;
    const toClose = editorState.openFiles.filter((f) =>
      f.path.startsWith(removedPath + "/") || f.path.startsWith(removedPath + "\\") || f.path === removedPath,
    );
    for (const f of toClose) {
      closeFile(f.path);
    }
    if (toClose.length > 0) {
      notifyWarning(
        translate("projects.filesClosedNotice", { count: String(toClose.length), name: data.name ?? removedPath }),
      );
    }
  });
}

export function cleanupProjectRemovedListener(): void {
  projectListenerGeneration += 1;
  appInternals.projectRemovedListenerRegistered = false;
}

export function unregisterAppListeners(): void {
  appActionGeneration += 1;
  stopAppActionWatchers();
  // P9-G11: flush a pending settings save on teardown so debounce-pending
  // changes are not lost when a window closes (no-op when idle).
  void flushSettingsSave();
  stopSystemModeListener();
  stopWindowMaximiseListener();
  cleanupSettingsSyncListener();
  cleanupProjectRemovedListener();
}

// ---------------------------------------------------------------------------
// AI Config management
// ---------------------------------------------------------------------------

export function generateAIConfigId(): string {
  return `cfg_${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 8)}`;
}

export function activateAIConfig(id: string): void {
  const cfg = appState.aiProviderConfigs.find((c) => c.id === id);
  if (!cfg) return;
  appState.activeAIConfigId = id;
  appState.aiApiKey = "";
  appState.aiApiKeyConfigured = !!cfg.apiKeyConfigured;
  appState.aiBaseUrl = cfg.baseUrl;
  appState.aiModel = cfg.model;
  appState.temperature = cfg.temperature ?? 0.7;
  appState.reasoningEffort = cfg.reasoningEffort ?? "";
  appState.maxTokens = cfg.maxTokens ?? 4096;
  appState.aiSystemPrompt = cfg.systemPrompt ?? "";
  saveSettings();
}

export function saveAIConfig(cfg: AIProviderConfig): void {
  const idx = appState.aiProviderConfigs.findIndex((c) => c.id === cfg.id);
  if (idx >= 0) {
    appState.aiProviderConfigs[idx] = cfg;
  } else {
    appState.aiProviderConfigs.push(cfg);
  }
  if (appState.activeAIConfigId === cfg.id) {
    activateAIConfig(cfg.id);
  } else {
    saveSettings();
  }
}

export function deleteAIConfig(id: string): boolean {
  if (appState.aiProviderConfigs.length <= 1) return false;
  const idx = appState.aiProviderConfigs.findIndex((c) => c.id === id);
  if (idx < 0) return false;
  appState.aiProviderConfigs.splice(idx, 1);
  if (appState.activeAIConfigId === id) {
    const next = appState.aiProviderConfigs[0];
    if (next) activateAIConfig(next.id);
  } else {
    saveSettings();
  }
  return true;
}

export function createNewAIConfig(provider: string = "openai"): AIProviderConfig {
  const preset = PROVIDER_PRESETS.find((p) => p.id === provider);
  return {
    id: generateAIConfigId(),
    name: preset?.label ?? "New Config",
    provider,
    protocol: preset?.protocol ?? "openai",
    apiKey: "",
    baseUrl: preset?.baseUrl ?? "",
    model: preset?.models?.[0] ?? "",
    temperature: 0.7,
    reasoningEffort: "",
    maxTokens: 4096,
    systemPrompt: "",
  };
}

export function activeAIConfig(): AIProviderConfig | undefined {
  return appState.aiProviderConfigs.find((c) => c.id === appState.activeAIConfigId);
}

// ---------------------------------------------------------------------------
// Panel tab / extension view
// ---------------------------------------------------------------------------

export function setPanelTab(tab: PanelTab): void {
  appState.panelTab = tab;
}

export function setActiveExtensionView(viewId: string | null): void {
  appState.activeExtensionView = viewId;
}

export function setExtensionsSubview(view: ExtensionsSubview): void {
  appState.extensionsSubview = view;
}

// ---------------------------------------------------------------------------
// Watch 块（延迟初始化，避免模块顶层循环依赖）
// ---------------------------------------------------------------------------

/**
 * 初始化 app 级 watch 副作用。必须在 appState 聚合定义完成且
 * appActions 模块加载后调用一次（app.ts 末尾调用）。
 *
 * 包含：
 *   - theme 变化 → applyMode
 *   - currentProject 变化 → loadRules / clearRules + setActiveWorkspaceRoot
 */
export function initAppActions(): void {
  appActionGeneration += 1;
  stopAppActionWatchers();
  // Apply mode whenever theme changes (e.g. after loadSettings populates appState)
  appActionWatchStops.push(watch(
    () => appState.theme,
    (newMode) => {
      applyMode(newMode as ThemeMode);
    },
  ));

  // Load/clear project-level AI rules when the active project changes.
  appActionWatchStops.push(watch(
    () => appState.currentProject,
    (newPath) => {
      const generation = appActionGeneration;
      if (newPath) {
        void loadRules(newPath, () => generation === appActionGeneration);
        void import("@/stores/workspaceModules").then(({ setActiveWorkspaceRoot }) => {
          if (generation === appActionGeneration) {
            void setActiveWorkspaceRoot(newPath);
          }
        });
      } else {
        clearRules();
      }
    },
  ));
}


