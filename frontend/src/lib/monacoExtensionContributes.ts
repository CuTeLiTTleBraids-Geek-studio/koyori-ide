/**
 * F-3 (prompt-2.md): Monaco 集成层 — 将 VS Code 扩展 contributes.grammars
 * 和 contributes.snippets 注入 Monaco 编辑器。
 *
 * 职责：
 *   1. grammars → Monaco 语言注册
 *      - 当扩展贡献了 grammar 且其 language 尚未在 Monaco 中注册时，
 *        通过 monaco.languages.register() 注册该语言。
 *      - 记录 scopeName → language 映射，供未来 TextMate tokenization 使用。
 *   2. snippets → Monaco snippet completion provider
 *      - 通过后端 API 异步读取扩展中的 snippet 文件（VS Code snippet JSON 格式）。
 *      - 解析 snippet 文件，将每个 snippet 转换为 Monaco CompletionItem。
 *      - 使用 monaco.languages.registerCompletionItemProvider() 注册 provider，
 *        使 snippet 在编辑器中通过前缀触发补全。
 *
 * 调用时机：由 vscodeExtensionActivation.ts 的 activateExtensions() 在
 * injectContributes 完成后调用。使用动态 import 避免循环依赖。
 */
// Koyori IDE 模块 · Monaco Extension Contributes；交互服务：插件市场（MarketplaceService）。
// 喵，这是 Koyori IDE 的 Monaco Extension Contributes 模块（前端实现）~

import * as monaco from "monaco-editor";
import { marketplaceService } from "@/api/services";
import {
  listAllVscodeExtensionGrammars,
  listAllVscodeExtensionSnippets,
  type RegisteredSnippet,
} from "@/lib/vscodeExtensions";

// ---------------------------------------------------------------------------
// 状态
// ---------------------------------------------------------------------------

/** 已注册到 Monaco 的语言 ID 集合（避免重复注册）。 */
const registeredLanguages = new Set<string>();

/** scopeName → language ID 映射（供未来 TextMate tokenization 集成使用）。 */
const scopeToLanguage = new Map<string, string>();

/** 已注册的 snippet completion provider disposable，按 "language" 索引。 */
const snippetProviders = new Map<string, monaco.IDisposable>();

/** 已成功解析的 snippet 文件缓存，key 为 "extensionId:path"。 */
const snippetFileCache = new Map<string, ParsedSnippetItem[]>();
let snippetSyncGeneration = 0;

// ---------------------------------------------------------------------------
// Grammars → Monaco 语言注册
// ---------------------------------------------------------------------------

/**
 * F-3: 将 registry 中所有 grammars 同步到 Monaco。对于每个 grammar：
 *   - 如果其 language 非空且 Monaco 中尚未注册该语言，则注册之。
 *   - 记录 scopeName → language 映射。
 *
 * 注意：真正的 TextMate grammar tokenization 需要 monaco-textmate 库，
 * 超出 F-3 范围。F-3 聚焦语言 ID 注册与映射记录，使 Monaco 知道该语言存在。
 */
export function syncExtensionGrammarsToMonaco(): void {
  const allGrammars = listAllVscodeExtensionGrammars();
  scopeToLanguage.clear();
  for (const [language, grammarList] of Object.entries(allGrammars)) {
    // 如果 language 非空且 Monaco 中尚未注册该语言，注册它。
    if (language && !registeredLanguages.has(language)) {
      const existingLangs = monaco.languages.getLanguages();
      if (!existingLangs.some((l) => l.id === language)) {
        monaco.languages.register({ id: language });
      }
      registeredLanguages.add(language);
    }
    // 记录 scopeName → language 映射。
    for (const grammar of grammarList) {
      if (grammar.scopeName) {
        scopeToLanguage.set(grammar.scopeName, language);
      }
    }
  }
}

/**
 * F-3: 查询指定 scopeName 对应的 language ID。
 * 供未来 TextMate tokenization 集成使用。
 */
export function getLanguageForScope(scopeName: string): string | undefined {
  return scopeToLanguage.get(scopeName);
}

// ---------------------------------------------------------------------------
// Snippets → Monaco snippet completion provider
// ---------------------------------------------------------------------------

/** VS Code snippet 文件中单个 snippet 的结构。 */
interface VSCodeSnippet {
  prefix: string | string[];
  body: string[] | string;
  description?: string;
}

/** 已解析的 snippet 补全项。 */
interface ParsedSnippetItem {
  label: string;
  insertText: string;
  documentation: string;
}

/**
 * F-3: 将 registry 中所有 snippets 同步到 Monaco。对于每个语言：
 *   - 通过后端 API 异步读取所有 snippet 文件。
 *   - 解析 VS Code snippet JSON 格式。
 *   - 注册 Monaco completion provider，使 snippet 在编辑器中通过前缀触发补全。
 *
 * 已加载过的 snippet 路径不会重复加载。已注册 provider 的语言不会重复注册。
 */
export async function syncExtensionSnippetsToMonaco(): Promise<void> {
  const generation = ++snippetSyncGeneration;
  const allSnippets = listAllVscodeExtensionSnippets();
  const activeCacheKeys = new Set<string>();
  const pendingLoads: Array<{ snippet: RegisteredSnippet; cacheKey: string }> = [];

  for (const [language, snippetList] of Object.entries(allSnippets)) {
    if (!language) continue;
    for (const snippet of snippetList) {
      const cacheKey = `${snippet.extensionId}:${snippet.path}`;
      activeCacheKeys.add(cacheKey);
      if (!snippetFileCache.has(cacheKey)) {
        pendingLoads.push({ snippet, cacheKey });
      }
    }
  }

  // 并发加载所有未缓存的 snippet 文件。失败项不进入缓存，下次同步会重试。
  const loadResults = await Promise.allSettled(
    pendingLoads.map(({ snippet }) => loadSnippetFile(snippet)),
  );
  if (generation !== snippetSyncGeneration) return;

  for (const [index, result] of loadResults.entries()) {
    if (result.status === "fulfilled") {
      snippetFileCache.set(pendingLoads[index].cacheKey, result.value);
    }
  }

  // 删除 registry 中已不存在的扩展文件缓存。
  for (const cacheKey of snippetFileCache.keys()) {
    if (!activeCacheKeys.has(cacheKey)) {
      snippetFileCache.delete(cacheKey);
    }
  }

  // 对账当前 registry 与已注册 provider，停用扩展后立即移除旧 snippet。
  const languages = new Set([
    ...Object.keys(allSnippets),
    ...snippetProviders.keys(),
  ]);
  for (const language of languages) {
    const items = (allSnippets[language] ?? []).flatMap((snippet) =>
      snippetFileCache.get(`${snippet.extensionId}:${snippet.path}`) ?? [],
    );
    const existing = snippetProviders.get(language);
    if (existing) {
      existing.dispose();
      snippetProviders.delete(language);
    }
    if (items.length === 0) continue;

    try {
      const provider = monaco.languages.registerCompletionItemProvider(language, {
        triggerCharacters: ["a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o", "p", "q", "r", "s", "t", "u", "v", "w", "x", "y", "z"],
        provideCompletionItems: (model, position) => {
          const word = model.getWordUntilPosition(position);
          const range = {
            startLineNumber: position.lineNumber,
            endLineNumber: position.lineNumber,
            startColumn: word.startColumn,
            endColumn: word.endColumn,
          };
          return {
            suggestions: items.map((item) => ({
              label: item.label,
              kind: monaco.languages.CompletionItemKind.Snippet,
              insertText: item.insertText,
              insertTextRules: monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet,
              documentation: item.documentation,
              range,
              sortText: "z", // snippet 排在普通补全之后
            })),
          };
        },
      });
      snippetProviders.set(language, provider);
    } catch (err) {
      console.warn(`[F-3] Failed to register snippet provider for ${language}:`, err);
    }
  }
}

/**
 * F-3: 异步加载单个 snippet 文件并解析为 ParsedSnippetItem 列表。
 * VS Code snippet 文件格式：{ "snippet_name": { "prefix": "...", "body": [...], "description": "..." } }
 */
async function loadSnippetFile(snippet: RegisteredSnippet): Promise<ParsedSnippetItem[]> {
  const [publisher, name] = snippet.extensionId.split(".");
  if (!publisher || !name) return [];
  try {
    const data = await marketplaceService.readExtensionFile(publisher, name, snippet.path);
    const text = new TextDecoder().decode(data);
    const parsed = JSON.parse(text) as Record<string, VSCodeSnippet>;
    const items: ParsedSnippetItem[] = [];
    for (const [key, value] of Object.entries(parsed)) {
      if (!value.prefix || !value.body) continue;
      // prefix 可以是字符串或字符串数组，取第一个作为 label。
      const prefix = Array.isArray(value.prefix) ? value.prefix[0] : value.prefix;
      if (!prefix) continue;
      const body = Array.isArray(value.body) ? value.body.join("\n") : value.body;
      items.push({
        label: prefix,
        insertText: body,
        documentation: value.description || key,
      });
    }
    return items;
  } catch (err) {
    console.warn(
      `[F-3] Failed to load snippet file ${snippet.path} from ${snippet.extensionId}:`,
      err,
    );
    throw err;
  }
}

// ---------------------------------------------------------------------------
// 清理
// ---------------------------------------------------------------------------

/**
 * F-3: 清理所有 Monaco 扩展 contributes 集成状态。dispose 所有 snippet
 * provider，清空语言注册和映射。在扩展全部卸载或测试中调用。
 */
export function clearMonacoExtensionContributes(): void {
  snippetSyncGeneration += 1;
  for (const provider of snippetProviders.values()) {
    provider.dispose();
  }
  snippetProviders.clear();
  registeredLanguages.clear();
  scopeToLanguage.clear();
  snippetFileCache.clear();
}
