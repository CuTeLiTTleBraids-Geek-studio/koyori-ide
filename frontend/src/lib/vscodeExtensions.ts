/**
 * G-VSC-04: VS Code extension registry — frontend counterpart to a future
 * Extension Host bridge.
 *
 * The IDE hosts two coexisting extension systems:
 *   1. koyori-ide native plugins (pluginRegistry.ts) — permission-gated,
 *      sandboxed, higher priority.
 *   2. VS Code extensions — run in the Extension Host process, broader
 *      capabilities, supplementary.
 *
 * This module is the integration point the Extension Host bridge calls into.
 * It registers VS Code extension commands and installed-extension metadata so
 * that `unifiedCommands.ts` can aggregate them into one command palette and
 * `PluginManagementPanel.vue` can render a unified management UI.
 *
 * Reactivity: mirrors pluginRegistry's pattern — a version ref is bumped on
 * every mutation so Vue computeds that call listVscodeExtensionCommands() /
 * listVscodeExtensions() re-evaluate. Until a real Extension Host is wired up,
 * the registry stays empty; the aggregation code handles that gracefully.
 */
// Koyori IDE 模块 · Vscode Extensions。
// 喵，这是 Koyori IDE 的 Vscode Extensions 模块（前端实现）~

import { ref } from "vue";
import type {
  ExtensionViewContribution,
  ExtensionGrammarContribution,
  ExtensionSnippetContribution,
  VscodeExtensionCommand,
  VscodeExtensionInfo,
  VscodeExtensionSecurityLevel,
} from "@/types";

// ---------------------------------------------------------------------------
// Registry state
// ---------------------------------------------------------------------------

const extensions = new Map<string, VscodeExtensionInfo>();
const commands = new Map<string, VscodeExtensionCommand>();
// F-3 (prompt-2.md): VS Code 扩展视图 registry，按容器 ID 分组。
// key = 容器 ID（如 "explorer"、"debug"），value = 该容器下的视图列表。
const views = new Map<string, RegisteredView[]>();
// F-3: VS Code 扩展 grammar registry，按 language ID 分组。
// key = language ID（如 "go"、"rust"），value = 该语言的 grammar 列表。
// 条目附带 extensionId，Monaco 集成层用此读取 grammar 文件。
const grammars = new Map<string, RegisteredGrammar[]>();
// F-3: VS Code 扩展 snippet registry，按 language ID 分组。
// key = language ID，value = 该语言的 snippet contribution 列表。
// 条目附带 extensionId，Monaco 集成层用此读取 snippet 文件。
const snippets = new Map<string, RegisteredSnippet[]>();

// G-VSC-04: Reactive version counters bumped on every mutation so Vue
// computeds that consume the registry re-evaluate (same pattern as
// pluginRegistry's commandsVersion / viewsVersion).
const extensionsVersion = ref(0);
const commandsVersion = ref(0);
// F-3: 视图版本计数器，ActivityBar / SidePanel 用此建立响应式依赖。
const viewsVersion = ref(0);
// F-3: grammar/snippet 版本计数器，Monaco 集成层用此建立响应式依赖。
const grammarsVersion = ref(0);
const snippetsVersion = ref(0);

// ---------------------------------------------------------------------------
// Extension metadata
// ---------------------------------------------------------------------------

/**
 * Register (or upsert) an installed VS Code extension. Called by the
 * Extension Host bridge after it discovers installed extensions. Re-registering
 * an existing id updates its metadata in place.
 */
export function registerVscodeExtension(info: VscodeExtensionInfo): void {
  extensions.set(info.id, { ...info });
  extensionsVersion.value++;
}

/**
 * Remove a VS Code extension from the registry. Also unregisters any commands
 * owned by it. Called when an extension is uninstalled.
 */
export function unregisterVscodeExtension(id: string): void {
  let changed = extensions.delete(id);
  for (const [cmdId, cmd] of Array.from(commands.entries())) {
    if (cmd.extensionId === id) {
      commands.delete(cmdId);
      changed = true;
    }
  }
  if (changed) {
    extensionsVersion.value++;
    commandsVersion.value++;
  }
}

/**
 * List all known VS Code extensions. Reads extensionsVersion to establish a
 * reactive dependency.
 */
export function listVscodeExtensions(): VscodeExtensionInfo[] {
  void extensionsVersion.value; // track for reactivity
  return Array.from(extensions.values());
}

/**
 * Enable or disable a VS Code extension. Persists only in-memory; the
 * Extension Host bridge is responsible for actually (de)activating it.
 */
export function setVscodeExtensionEnabled(id: string, enabled: boolean): void {
  const ext = extensions.get(id);
  if (!ext) return;
  ext.enabled = enabled;
  extensionsVersion.value++;
}

/** Look up a single extension by id. */
export function getVscodeExtension(id: string): VscodeExtensionInfo | undefined {
  return extensions.get(id);
}

// ---------------------------------------------------------------------------
// Commands
// ---------------------------------------------------------------------------

/**
 * Register a command contributed by a VS Code extension. The Extension Host
 * bridge calls this for each command an extension declares. Re-registering the
 * same id by the same extension is idempotent; a different extension id wins
 * the slot only if the previous owner was unregistered.
 */
export function registerVscodeExtensionCommand(
  cmd: VscodeExtensionCommand,
): void {
  const existing = commands.get(cmd.id);
  if (existing && existing.extensionId !== cmd.extensionId) {
    throw new Error(
      `VS Code command "${cmd.id}" is already registered by extension "${existing.extensionId}"`,
    );
  }
  commands.set(cmd.id, { ...cmd });
  commandsVersion.value++;
}

/** Remove a single VS Code extension command by id. */
export function unregisterVscodeExtensionCommand(id: string): void {
  if (commands.delete(id)) {
    commandsVersion.value++;
  }
}

/**
 * List all commands contributed by VS Code extensions. Reads commandsVersion
 * to establish a reactive dependency.
 */
export function listVscodeExtensionCommands(): VscodeExtensionCommand[] {
  void commandsVersion.value; // track for reactivity
  return Array.from(commands.values());
}

/**
 * Execute a VS Code extension command by id. The handler registered via
 * registerVscodeExtensionCommand is invoked directly; the Extension Host
 * bridge is responsible for ensuring the owning extension is active.
 */
export async function executeVscodeExtensionCommand(
  id: string,
  ...args: unknown[]
): Promise<unknown> {
  // The descriptor can be registered from a manifest before its worker is
  // active. Its handler owns the lazy activation boundary.
  const cmd = commands.get(id);
  if (!cmd) {
    throw new Error(`VS Code extension command "${id}" is not registered`);
  }
  return cmd.handler(...args);
}

// ---------------------------------------------------------------------------
// Views (F-3, prompt-2.md)
// ---------------------------------------------------------------------------

/**
 * F-3: 注册一组 VS Code 扩展视图到指定容器。同一容器的视图按注册顺序追加；
 * 同一扩展重复注册同一 view id 会先移除旧条目再追加，保证幂等且不覆盖其他扩展。调用方为扩展激活编排器
 * （vscodeExtensionActivation.ts），它在解析 contributes.views 后调用此方法。
 */
export function registerVscodeExtensionViews(
  extensionId: string,
  container: string,
  viewList: ExtensionViewContribution[],
): void {
  if (!viewList || viewList.length === 0) return;
  const existing = views.get(container) ?? [];
  const incoming = Array.from(
    new Map(viewList.map((view) => [view.id, { ...view, extensionId }])).values(),
  );
  const incomingIds = new Set(incoming.map((view) => view.id));
  const filtered = existing.filter(
    (view) => view.extensionId !== extensionId || !incomingIds.has(view.id),
  );
  views.set(container, [...filtered, ...incoming]);
  viewsVersion.value++;
}

/**
 * F-3: 移除指定容器中由某扩展注册的所有视图。调用方为扩展卸载/停用路径。
 */
export function unregisterVscodeExtensionViews(
  extensionId: string,
  container: string,
  viewIds: string[],
): void {
  if (!viewIds || viewIds.length === 0) return;
  const existing = views.get(container);
  if (!existing) return;
  const idSet = new Set(viewIds);
  const filtered = existing.filter(
    (view) => view.extensionId !== extensionId || !idSet.has(view.id),
  );
  if (filtered.length === 0) {
    views.delete(container);
  } else {
    views.set(container, filtered);
  }
  viewsVersion.value++;
}

/**
 * F-3: 列出指定容器下的所有 VS Code 扩展视图。读取 viewsVersion 建立响应式
 * 依赖。ActivityBar / SidePanel 用此获取动态视图列表。
 */
export function listVscodeExtensionViews(
  container: string,
): RegisteredView[] {
  void viewsVersion.value; // track for reactivity
  return views.get(container) ?? [];
}

/**
 * F-3: 列出所有容器的视图，按容器 ID 分组返回。用于调试/管理面板。
 */
export function listAllVscodeExtensionViews(): Record<string, RegisteredView[]> {
  void viewsVersion.value;
  const out: Record<string, RegisteredView[]> = {};
  for (const [container, list] of views.entries()) {
    out[container] = [...list];
  }
  return out;
}

export interface RegisteredView extends ExtensionViewContribution {
  extensionId: string;
}

// ---------------------------------------------------------------------------
// Grammars (F-3, prompt-2.md) — contributes.grammars → Monaco 语言配置
// ---------------------------------------------------------------------------

/**
 * F-3: 注册到 registry 的 grammar 条目，附带来源扩展 ID。Monaco 集成层
 * 用 extensionId 拆分为 publisher/name，通过后端 API 读取 grammar 文件。
 */
export interface RegisteredGrammar extends ExtensionGrammarContribution {
  extensionId: string;
}

/**
 * F-3: 注册一组 VS Code 扩展 grammar 到指定 language ID。同一语言的 grammar
 * 按注册顺序追加；同一扩展重复注册同一 scopeName 会先移除旧条目再追加，保证幂等且不覆盖其他扩展。
 * 调用方为扩展激活编排器（vscodeExtensionActivation.ts），它在解析
 * contributes.grammars 后调用此方法。Monaco 集成层随后通过
 * listVscodeExtensionGrammars(language) 读取并注入 TextMate grammar。
 *
 * 注意：grammars 的 `language` 字段可选（注入式 grammar 无 language）。
 * 此函数按传入的 language 参数分组；language 为空字符串时表示注入式
 * grammar，单独存储在 "" key 下。
 */
export function registerVscodeExtensionGrammars(
  extensionId: string,
  language: string,
  grammarList: ExtensionGrammarContribution[],
): void {
  if (!grammarList || grammarList.length === 0) return;
  const existing = grammars.get(language) ?? [];
  const annotated = Array.from(
    new Map(
      grammarList.map((grammar) => [
        grammar.scopeName,
        { ...grammar, extensionId },
      ]),
    ).values(),
  );
  const existingScopes = new Set(annotated.map((grammar) => grammar.scopeName));
  const filtered = existing.filter(
    (grammar) =>
      grammar.extensionId !== extensionId ||
      !existingScopes.has(grammar.scopeName),
  );
  grammars.set(language, [...filtered, ...annotated]);
  grammarsVersion.value++;
}

/**
 * F-3: 移除指定 language 中由某扩展注册的所有 grammar（按 scopeName 标识）。
 * 调用方为扩展卸载/停用路径。
 */
export function unregisterVscodeExtensionGrammars(
  extensionId: string,
  language: string,
  scopeNames: string[],
): void {
  if (!scopeNames || scopeNames.length === 0) return;
  const existing = grammars.get(language);
  if (!existing) return;
  const scopeSet = new Set(scopeNames);
  const filtered = existing.filter(
    (grammar) =>
      grammar.extensionId !== extensionId || !scopeSet.has(grammar.scopeName),
  );
  if (filtered.length === 0) {
    grammars.delete(language);
  } else {
    grammars.set(language, filtered);
  }
  grammarsVersion.value++;
}

/**
 * F-3: 列出指定 language 的所有 VS Code 扩展 grammar。读取 grammarsVersion
 * 建立响应式依赖。Monaco 集成层用此获取 grammar 列表并注入 TextMate
 * tokenization 配置。返回的条目附带 extensionId，用于读取 grammar 文件。
 */
export function listVscodeExtensionGrammars(
  language: string,
): RegisteredGrammar[] {
  void grammarsVersion.value; // track for reactivity
  return grammars.get(language) ?? [];
}

/**
 * F-3: 列出所有语言的 grammar，按 language ID 分组返回。用于调试/管理面板
 * 以及 Monaco 集成层的全量初始化。
 */
export function listAllVscodeExtensionGrammars(): Record<string, RegisteredGrammar[]> {
  void grammarsVersion.value;
  const out: Record<string, RegisteredGrammar[]> = {};
  for (const [language, list] of grammars.entries()) {
    out[language] = [...list];
  }
  return out;
}

// ---------------------------------------------------------------------------
// Snippets (F-3, prompt-2.md) — contributes.snippets → Monaco snippet 注册表
// ---------------------------------------------------------------------------

/**
 * F-3: 注册到 registry 的 snippet 条目，附带来源扩展 ID。Monaco 集成层
 * 用 extensionId 拆分为 publisher/name，通过后端 API 读取 snippet 文件。
 */
export interface RegisteredSnippet extends ExtensionSnippetContribution {
  extensionId: string;
}

/**
 * F-3: 注册一组 VS Code 扩展 snippet 到指定 language ID。同一语言的 snippet
 * 按注册顺序追加；同一扩展重复注册同一 path 会先移除旧条目再追加，保证幂等且不覆盖其他扩展。
 * 调用方为扩展激活编排器（vscodeExtensionActivation.ts），它在解析
 * contributes.snippets 后调用此方法。Monaco 集成层随后通过
 * listVscodeExtensionSnippets(language) 读取并加载 snippet 文件到 Monaco
 * snippet 注册表。
 */
export function registerVscodeExtensionSnippets(
  extensionId: string,
  language: string,
  snippetList: ExtensionSnippetContribution[],
): void {
  if (!snippetList || snippetList.length === 0) return;
  const existing = snippets.get(language) ?? [];
  const annotated = Array.from(
    new Map(
      snippetList.map((snippet) => [
        snippet.path,
        { ...snippet, extensionId },
      ]),
    ).values(),
  );
  const existingPaths = new Set(annotated.map((snippet) => snippet.path));
  const filtered = existing.filter(
    (snippet) =>
      snippet.extensionId !== extensionId || !existingPaths.has(snippet.path),
  );
  snippets.set(language, [...filtered, ...annotated]);
  snippetsVersion.value++;
}

/**
 * F-3: 移除指定 language 中由某扩展注册的所有 snippet（按 path 标识）。
 * 调用方为扩展卸载/停用路径。
 */
export function unregisterVscodeExtensionSnippets(
  extensionId: string,
  language: string,
  paths: string[],
): void {
  if (!paths || paths.length === 0) return;
  const existing = snippets.get(language);
  if (!existing) return;
  const pathSet = new Set(paths);
  const filtered = existing.filter(
    (snippet) =>
      snippet.extensionId !== extensionId || !pathSet.has(snippet.path),
  );
  if (filtered.length === 0) {
    snippets.delete(language);
  } else {
    snippets.set(language, filtered);
  }
  snippetsVersion.value++;
}

/**
 * F-3: 列出指定 language 的所有 VS Code 扩展 snippet。读取 snippetsVersion
 * 建立响应式依赖。Monaco 集成层用此获取 snippet 列表并加载 snippet 文件
 * 到 Monaco snippet 注册表。返回的条目附带 extensionId，用于读取 snippet 文件。
 */
export function listVscodeExtensionSnippets(
  language: string,
): RegisteredSnippet[] {
  void snippetsVersion.value; // track for reactivity
  return snippets.get(language) ?? [];
}

/**
 * F-3: 列出所有语言的 snippet，按 language ID 分组返回。用于调试/管理面板
 * 以及 Monaco 集成层的全量初始化。
 */
export function listAllVscodeExtensionSnippets(): Record<string, RegisteredSnippet[]> {
  void snippetsVersion.value;
  const out: Record<string, RegisteredSnippet[]> = {};
  for (const [language, list] of snippets.entries()) {
    out[language] = [...list];
  }
  return out;
}

// ---------------------------------------------------------------------------
// Helpers / test utilities
// ---------------------------------------------------------------------------

/**
 * Map a VscodeExtensionSecurityLevel to a user-facing badge label key. Used by
 * the management panel to render the security-level tag. The level vocabulary
 * mirrors the G-VSC-03 extensionSecurity store (trusted / reviewed / restricted).
 */
export function securityLevelLabel(
  level: VscodeExtensionSecurityLevel,
): "trusted" | "reviewed" | "restricted" {
  return level;
}

/** Clear the entire registry. Used in tests and on full project switch. */
export function clearVscodeExtensions(): void {
  extensions.clear();
  commands.clear();
  views.clear();
  grammars.clear();
  snippets.clear();
  extensionsVersion.value++;
  commandsVersion.value++;
  viewsVersion.value++;
  grammarsVersion.value++;
  snippetsVersion.value++;
}
