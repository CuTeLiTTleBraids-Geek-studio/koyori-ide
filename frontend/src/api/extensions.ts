// Koyori IDE 模块 · Extensions。
// 喵，这是 Koyori IDE 的 Extensions 模块（前端实现）~
import * as MarketplaceServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/marketplaceservice.js";
import * as SymbolIndexServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/symbolindexservice.js";
import type { ExtensionDetail, IndexStats, VSCodeExtensionManifest } from "@/types";
import type { ExtensionPermission } from "@/lib/extensionHost/permissions";
import {
  type UnknownRecord, decodeWailsBytes, isRecord, optionalString,
  optionalStringArray, optionalStringRecord, requiredString,
  safeRecordFromEntries, requireNonNull, unwrapNullable,
  warnInvalidBoundaryValue,
} from "./boundary";

type BindingExtensionManifest = NonNullable<
  Awaited<ReturnType<typeof MarketplaceServiceBindings.GetExtensionManifest>>
>;
type FrontendExtensionContributes = NonNullable<
  VSCodeExtensionManifest["parsedContributes"]
>;
type FrontendExtensionLanguage = NonNullable<
  FrontendExtensionContributes["languages"]
>[number];
type FrontendExtensionGrammar = NonNullable<
  FrontendExtensionContributes["grammars"]
>[number];
type FrontendExtensionSnippet = NonNullable<
  FrontendExtensionContributes["snippets"]
>[number];
type FrontendExtensionCommand = NonNullable<
  FrontendExtensionContributes["commands"]
>[number];
type FrontendExtensionConfiguration = NonNullable<
  FrontendExtensionContributes["configuration"]
>[number];
type FrontendExtensionDebugger = NonNullable<
  FrontendExtensionContributes["debuggers"]
>[number];
type FrontendExtensionJSONValidation = NonNullable<
  FrontendExtensionContributes["jsonValidation"]
>[number];
type FrontendExtensionView = NonNullable<
  NonNullable<FrontendExtensionContributes["views"]>[string]
>[number];
type FrontendExtensionViewWelcome = NonNullable<
  FrontendExtensionContributes["viewsWelcome"]
>[number];
type FrontendExtensionMenu = NonNullable<
  NonNullable<FrontendExtensionContributes["menus"]>[string]
>[number];
type FrontendExtensionKeybinding = NonNullable<
  FrontendExtensionContributes["keybindings"]
>[number];
type FrontendExtensionTheme = NonNullable<
  FrontendExtensionContributes["themes"]
>[number];
type FrontendExtensionIconTheme = NonNullable<
  FrontendExtensionContributes["iconThemes"]
>[number];

function normalizeContributionArray<T>(
  value: unknown,
  path: string,
  convert: (entry: unknown, path: string) => T,
): T[] | undefined {
  if (value === undefined || value === null) return undefined;
  if (!Array.isArray(value)) {
    warnInvalidBoundaryValue(path, "an array of extension contributions", "undefined");
    return undefined;
  }
  return value.map((entry, index) => convert(entry, `${path}[${index}]`));
}

function normalizeContributionMap<T>(
  value: unknown,
  path: string,
  convert: (entry: unknown, path: string) => T,
): Record<string, T[]> | undefined {
  if (value === undefined || value === null) return undefined;
  if (!isRecord(value)) {
    warnInvalidBoundaryValue(path, "an extension contribution map", "undefined");
    return undefined;
  }
  const normalizedEntries: Array<[string, T[]]> = [];
  for (const [key, contributionEntries] of Object.entries(value)) {
    normalizedEntries.push([
      key,
      normalizeContributionArray(contributionEntries, `${path}.${key}`, convert) ?? [],
    ]);
  }
  return safeRecordFromEntries(normalizedEntries);
}

function contributionRecord(value: unknown, path: string): UnknownRecord {
  if (isRecord(value)) return value;
  warnInvalidBoundaryValue(path, "an extension contribution object", "an empty object");
  return {};
}

function fromBindingParsedContributes(
  value: unknown,
  path: string,
): FrontendExtensionContributes | undefined {
  if (value === undefined || value === null) return undefined;
  if (!isRecord(value)) {
    warnInvalidBoundaryValue(path, "an extension contributes object", "undefined");
    return undefined;
  }
  return {
    languages: normalizeContributionArray(
      value.languages,
      `${path}.languages`,
      (entry, entryPath): FrontendExtensionLanguage => {
        const language = contributionRecord(entry, entryPath);
        return {
          id: requiredString(language.id, `${entryPath}.id`),
          aliases: optionalStringArray(language.aliases, `${entryPath}.aliases`),
          extensions: optionalStringArray(language.extensions, `${entryPath}.extensions`),
          configuration: optionalString(language.configuration, `${entryPath}.configuration`),
        };
      },
    ),
    grammars: normalizeContributionArray(
      value.grammars,
      `${path}.grammars`,
      (entry, entryPath): FrontendExtensionGrammar => {
        const grammar = contributionRecord(entry, entryPath);
        return {
          language: optionalString(grammar.language, `${entryPath}.language`),
          scopeName: requiredString(grammar.scopeName, `${entryPath}.scopeName`),
          path: requiredString(grammar.path, `${entryPath}.path`),
        };
      },
    ),
    snippets: normalizeContributionArray(
      value.snippets,
      `${path}.snippets`,
      (entry, entryPath): FrontendExtensionSnippet => {
        const snippet = contributionRecord(entry, entryPath);
        return {
          language: requiredString(snippet.language, `${entryPath}.language`),
          path: requiredString(snippet.path, `${entryPath}.path`),
        };
      },
    ),
    commands: normalizeContributionArray(
      value.commands,
      `${path}.commands`,
      (entry, entryPath): FrontendExtensionCommand => {
        const command = contributionRecord(entry, entryPath);
        return {
          command: requiredString(command.command, `${entryPath}.command`),
          title: requiredString(command.title, `${entryPath}.title`),
          category: optionalString(command.category, `${entryPath}.category`),
        };
      },
    ),
    configuration: normalizeContributionArray(
      value.configuration,
      `${path}.configuration`,
      (entry, entryPath): FrontendExtensionConfiguration => {
        const configuration = contributionRecord(entry, entryPath);
        return {
          title: optionalString(configuration.title, `${entryPath}.title`),
          properties: configuration.properties,
        };
      },
    ),
    debuggers: normalizeContributionArray(
      value.debuggers,
      `${path}.debuggers`,
      (entry, entryPath): FrontendExtensionDebugger => {
        const debug = contributionRecord(entry, entryPath);
        return {
          type: requiredString(debug.type, `${entryPath}.type`),
          label: optionalString(debug.label, `${entryPath}.label`),
          languages: optionalStringArray(debug.languages, `${entryPath}.languages`),
          configurationAttributes: debug.configurationAttributes,
        };
      },
    ),
    jsonValidation: normalizeContributionArray(
      value.jsonValidation,
      `${path}.jsonValidation`,
      (entry, entryPath): FrontendExtensionJSONValidation => {
        const validation = contributionRecord(entry, entryPath);
        return {
          fileMatch: requiredString(validation.fileMatch, `${entryPath}.fileMatch`),
          url: requiredString(validation.url, `${entryPath}.url`),
        };
      },
    ),
    views: normalizeContributionMap(
      value.views,
      `${path}.views`,
      (entry, entryPath): FrontendExtensionView => {
        const view = contributionRecord(entry, entryPath);
        return {
          id: requiredString(view.id, `${entryPath}.id`),
          name: requiredString(view.name, `${entryPath}.name`),
          when: optionalString(view.when, `${entryPath}.when`),
          icon: optionalString(view.icon, `${entryPath}.icon`),
          contextualTitle: optionalString(view.contextualTitle, `${entryPath}.contextualTitle`),
          visibility: optionalString(view.visibility, `${entryPath}.visibility`),
        };
      },
    ),
    viewsWelcome: normalizeContributionArray(
      value.viewsWelcome,
      `${path}.viewsWelcome`,
      (entry, entryPath): FrontendExtensionViewWelcome => {
        const welcome = contributionRecord(entry, entryPath);
        return {
          view: requiredString(welcome.view, `${entryPath}.view`),
          contents: requiredString(welcome.contents, `${entryPath}.contents`),
          when: optionalString(welcome.when, `${entryPath}.when`),
        };
      },
    ),
    menus: normalizeContributionMap(
      value.menus,
      `${path}.menus`,
      (entry, entryPath): FrontendExtensionMenu => {
        const menu = contributionRecord(entry, entryPath);
        return {
          command: requiredString(menu.command, `${entryPath}.command`),
          alt: optionalString(menu.alt, `${entryPath}.alt`),
          when: optionalString(menu.when, `${entryPath}.when`),
          group: optionalString(menu.group, `${entryPath}.group`),
        };
      },
    ),
    keybindings: normalizeContributionArray(
      value.keybindings,
      `${path}.keybindings`,
      (entry, entryPath): FrontendExtensionKeybinding => {
        const keybinding = contributionRecord(entry, entryPath);
        return {
          command: requiredString(keybinding.command, `${entryPath}.command`),
          key: requiredString(keybinding.key, `${entryPath}.key`),
          mac: optionalString(keybinding.mac, `${entryPath}.mac`),
          linux: optionalString(keybinding.linux, `${entryPath}.linux`),
          win: optionalString(keybinding.win, `${entryPath}.win`),
          when: optionalString(keybinding.when, `${entryPath}.when`),
          args: keybinding.args,
        };
      },
    ),
    themes: normalizeContributionArray(
      value.themes,
      `${path}.themes`,
      (entry, entryPath): FrontendExtensionTheme => {
        const theme = contributionRecord(entry, entryPath);
        return {
          label: requiredString(theme.label, `${entryPath}.label`),
          uiTheme: optionalString(theme.uiTheme, `${entryPath}.uiTheme`),
          path: requiredString(theme.path, `${entryPath}.path`),
        };
      },
    ),
    iconThemes: normalizeContributionArray(
      value.iconThemes,
      `${path}.iconThemes`,
      (entry, entryPath): FrontendExtensionIconTheme => {
        const theme = contributionRecord(entry, entryPath);
        return {
          id: requiredString(theme.id, `${entryPath}.id`),
          label: requiredString(theme.label, `${entryPath}.label`),
          path: requiredString(theme.path, `${entryPath}.path`),
        };
      },
    ),
  };
}

export function fromBindingExtensionManifest(
  manifest: BindingExtensionManifest,
): VSCodeExtensionManifest {
  const normalized: VSCodeExtensionManifest = {
    name: requiredString(manifest.name, "VSCodeExtensionManifest.name"),
    publisher: requiredString(manifest.publisher, "VSCodeExtensionManifest.publisher"),
    version: requiredString(manifest.version, "VSCodeExtensionManifest.version"),
    displayName: requiredString(manifest.displayName, "VSCodeExtensionManifest.displayName"),
    description: requiredString(manifest.description, "VSCodeExtensionManifest.description"),
    engines: optionalStringRecord(manifest.engines, "VSCodeExtensionManifest.engines") ?? {},
    activationEvents:
      optionalStringArray(
        manifest.activationEvents,
        "VSCodeExtensionManifest.activationEvents",
      ) ?? [],
    contributes: manifest.contributes,
    capabilities: manifest.capabilities,
    main: optionalString(manifest.main, "VSCodeExtensionManifest.main"),
    browser: optionalString(manifest.browser, "VSCodeExtensionManifest.browser"),
    koyoriIde: manifest.koyoriIde
      ? {
          permissions:
            manifest.koyoriIde.permissions?.map(
              (permission) => String(permission) as ExtensionPermission,
            ) ?? undefined,
        }
      : undefined,
  };
  normalized.parsedContributes = fromBindingParsedContributes(
    manifest.parsedContributes,
    "VSCodeExtensionManifest.parsedContributes",
  );
  return normalized;
}

// G-VSC-01: VS Code extension marketplace client (Open VSX by default).
// Installed extensions live under <configDir>/koyori-ide/extensions/ and are
// disabled by default (G-SEC-12 req. 2). Downloads are SHA-256 verified
// (req. 3); a mismatch aborts the install.
export const marketplaceService = {
  // Search the registry for extensions matching a query. page is 1-based.
  searchExtensions: (query: string, page: number, pageSize: number) =>
    unwrapNullable(MarketplaceServiceBindings.SearchExtensions(query, page, pageSize), []),
  // Fetch full metadata (categories, tags, versions, readme) for one extension.
  getExtensionDetail: async (publisher: string, name: string): Promise<ExtensionDetail> => {
    const detail = await requireNonNull(
      MarketplaceServiceBindings.GetExtensionDetail(publisher, name),
      "MarketplaceService.GetExtensionDetail",
    );
    return {
      ...detail,
      categories: detail.categories ?? [],
      tags: detail.tags ?? [],
      versions: detail.versions ?? [],
    };
  },
  // Download, SHA-256 verify, and install a VSIX. Newly installed extensions
  // start disabled. Pass an empty version to install the latest.
  downloadAndInstallExtension: (publisher: string, name: string, version: string) =>
    MarketplaceServiceBindings.DownloadAndInstallExtension(publisher, name, version) as Promise<void>,
  // Replace an installed extension through the backend's staged transaction.
  updateExtension: (publisher: string, name: string, version: string) =>
    MarketplaceServiceBindings.UpdateExtension(publisher, name, version) as Promise<void>,
  // List locally installed extensions with their enabled state (sorted by
  // publisher then name). Missing state entries default to disabled.
  listInstalledExtensions: () =>
    unwrapNullable(MarketplaceServiceBindings.ListInstalledExtensions(), []),
  // Remove an installed extension from disk and clear its enabled state.
  uninstallExtension: (publisher: string, name: string) =>
    MarketplaceServiceBindings.UninstallExtension(publisher, name) as Promise<void>,
  // Toggle the enabled/disabled state of an installed extension.
  setExtensionEnabled: (
    publisher: string,
    name: string,
    enabled: boolean,
    explicitApproval = false,
  ) =>
    MarketplaceServiceBindings.SetExtensionEnabled(
      publisher,
      name,
      enabled,
      explicitApproval,
    ) as Promise<void>,
  // Read and parse extension/package.json from an installed extension.
  getExtensionManifest: async (publisher: string, name: string) =>
    fromBindingExtensionManifest(await requireNonNull(
      MarketplaceServiceBindings.GetExtensionManifest(publisher, name),
      "MarketplaceService.GetExtensionManifest",
    )),
  // G-MKT-02: Browse extensions in a specific category, sorted by download count.
  browseByCategory: (category: string, page: number, pageSize: number) =>
    unwrapNullable(MarketplaceServiceBindings.BrowseByCategory(category, page, pageSize), []),
  // G-MKT-02: Get featured/popular extensions for the marketplace landing page.
  getFeaturedExtensions: () =>
    unwrapNullable(MarketplaceServiceBindings.GetFeaturedExtensions(), []),
  // G-MKT-02: Check installed extensions for available updates.
  checkForUpdates: () =>
    unwrapNullable(MarketplaceServiceBindings.CheckForUpdates(), []),
  // G-MKT-02: Fetch the README content for an extension.
  getExtensionReadme: (publisher: string, name: string) =>
    MarketplaceServiceBindings.GetExtensionReadme(publisher, name) as Promise<string>,
  // G-MKT-02: Get the list of standard extension categories.
  getCategories: () =>
    unwrapNullable(MarketplaceServiceBindings.GetCategories(), []),
  // F-3 (prompt-2.md): 批量获取所有已安装扩展的 manifest（含解析后的
  // ParsedContributes）。前端扩展宿主用此一次性获取所有 contributes.commands /
  // views / grammars / snippets 等以注入命令面板、侧边栏与 Monaco。
  // 使用正式生成的 ByID binding，避免运行时方法名漂移。
  getInstalledExtensionManifests: async () => {
    const manifests = await unwrapNullable(
      MarketplaceServiceBindings.GetInstalledExtensionManifests(),
      [],
    );
    return manifests.map(fromBindingExtensionManifest);
  },
  // F-3: 触发 onLanguage:<language> 激活事件，返回需要激活的扩展 ID 列表。
  triggerActivationOnLanguage: (language: string) =>
    unwrapNullable(MarketplaceServiceBindings.TriggerActivationOnLanguage(language), []),
  // F-3: 触发 onCommand:<command> 激活事件。
  triggerActivationOnCommand: (command: string) =>
    unwrapNullable(MarketplaceServiceBindings.TriggerActivationOnCommand(command), []),
  // F-3: 触发 workspaceContains:<glob> 激活事件，扫描 workspaceRoot。
  triggerActivationWorkspaceContains: (workspaceRoot: string) =>
    unwrapNullable(
      MarketplaceServiceBindings.TriggerActivationWorkspaceContains(workspaceRoot),
      [],
    ),
  // F-3: 触发 onDebug 激活事件。
  triggerActivationOnDebug: () =>
    unwrapNullable(MarketplaceServiceBindings.TriggerActivationOnDebug(), []),
  // F-3: 触发 onDebugResolve:<type> 激活事件。
  triggerActivationOnDebugResolve: (debugType: string) =>
    unwrapNullable(MarketplaceServiceBindings.TriggerActivationOnDebugResolve(debugType), []),
  // F-3: 触发 "*" eager 激活事件。调用方应提示用户确认。
  triggerActivationEager: () =>
    unwrapNullable(MarketplaceServiceBindings.TriggerActivationEager(), []),
  // F-3: 查询扩展是否已激活。
  isExtensionActivated: (extensionID: string) =>
    MarketplaceServiceBindings.IsExtensionActivated(extensionID),
  reportExtensionActivation: (extensionID: string, activated: boolean) =>
    MarketplaceServiceBindings.ReportExtensionActivation(extensionID, activated) as Promise<void>,
  reportExtensionDeactivated: (extensionID: string) =>
    MarketplaceServiceBindings.ReportExtensionDeactivated(extensionID) as Promise<void>,
  // F-3: 读取已安装扩展内的文件内容（如 snippet 文件、grammar 文件）。
  // relativePath 相对于扩展根目录。后端有路径穿越防护。
  readExtensionFile: async (publisher: string, name: string, relativePath: string) => {
    const raw = await unwrapNullable(
      MarketplaceServiceBindings.ReadExtensionFile(publisher, name, relativePath),
      "",
    );
    return decodeWailsBytes(raw);
  },
};

// G-COMP-01: Workspace symbol index for auto-import and symbol search.
// Scans Go/TS/JS files for exported symbols. Powers the "type b in a.js,
// press Enter, auto-import" feature and workspace symbol search (Ctrl+T).
export const symbolIndexService = {
  // Search indexed symbols by name (case-insensitive substring).
  // Pass empty query to list all (capped at limit; 0 = default 100).
  searchSymbols: (query: string, limit: number) =>
    unwrapNullable(SymbolIndexServiceBindings.SearchSymbols(query, limit), []),
  // Get symbols matching `name` that could be auto-imported into
  // fromFilePath. Excludes symbols already in scope (same file).
  getAutoImportCandidates: (name: string, fromFilePath: string) =>
    unwrapNullable(
      SymbolIndexServiceBindings.GetAutoImportCandidates(name, fromFilePath),
      [],
    ),
  // Diagnostic stats for the index (symbol count, file count, version).
  getIndexStats: () =>
    SymbolIndexServiceBindings.GetIndexStats() as Promise<IndexStats>,
};
