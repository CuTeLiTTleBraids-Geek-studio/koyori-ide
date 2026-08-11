// Koyori IDE 模块 · Lsp Completion。
// 喵，这是 Koyori IDE 的 Lsp Completion 模块（前端实现）~
import type * as monacoEditor from "monaco-editor";
import {
  getLSPCompletions,
  getLSPHover,
  getLSPDefinition,
  getLSPReferences,
  formatLSPDocument,
  closeLSPDocument,
  getLSPDocumentSymbols,
  resolveLSPCompletionItem,
  type LSPCodeAction,
} from "@/stores/lsp";
import { editorState } from "@/stores/editor";
import { updateContent } from "@/stores/editor";
import {
  getAutoImportCompletions,
  mergeAutoImportSuggestions,
} from "@/lib/autoImport";
import type {
  LSPCompletionItem,
  LSPCompletionRequest,
  LSPRange,
} from "@/types";
import { GetTriggerCharacters } from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/lspservice";
import { registerSemanticTokensProvider } from "@/lib/semanticTokens";
import { registerInlayHintsProvider } from "@/lib/inlayHints";
import {
  executeCodeLensCommand,
  registerCodeLensProvider,
  type CodeLensExecutionPayload,
} from "@/lib/codeLens";
import type { RefactorAction, RefactorRequest } from "@/stores/refactor";

export const LSP_REFACTOR_PREVIEW_COMMAND = "koyori-ide.refactor.preview";
export const LSP_CODE_ACTION_COMMAND = "koyori-ide.lsp.executeCodeAction";

interface RefactorPreviewCommandPayload {
  request: RefactorRequest;
  action: RefactorAction;
}

interface CodeActionResolutionContext {
  request: LSPCompletionRequest;
  source: LSPCodeAction;
  range: monacoEditor.IRange;
  diagnostics: monacoEditor.editor.IMarkerData[];
}

function hasCodeActionEdits(action: LSPCodeAction): boolean {
  return !!action.edit?.some((file) => file.edits?.length);
}

function hasResolvedCodeActionPayload(action: LSPCodeAction): boolean {
  return (
    hasCodeActionEdits(action) ||
    !!action.command ||
    !!action.preview?.files.length
  );
}

function needsCodeActionResolve(action: LSPCodeAction): boolean {
  return (
    action.data !== undefined &&
    action.data !== null &&
    !hasResolvedCodeActionPayload(action)
  );
}

function codeActionWorkspaceEdit(
  monaco: typeof import("monaco-editor"),
  action: LSPCodeAction,
): monacoEditor.languages.WorkspaceEdit | undefined {
  const edits: monacoEditor.languages.IWorkspaceTextEdit[] = [];
  for (const fileEdit of action.edit ?? []) {
    if (!fileEdit.filePath) continue;
    const resource = monaco.Uri.file(fileEdit.filePath);
    for (const edit of fileEdit.edits ?? []) {
      edits.push({
        resource,
        versionId: undefined,
        textEdit: {
          range: {
            startLineNumber: edit.startLine + 1,
            startColumn: edit.startCol + 1,
            endLineNumber: edit.endLine + 1,
            endColumn: edit.endCol + 1,
          },
          text: edit.newText,
        },
      });
    }
  }
  return edits.length ? { edits } : undefined;
}

function codeActionExecutionPayload(
  context: CodeActionResolutionContext,
  action: LSPCodeAction,
): CodeLensExecutionPayload | null {
  if (!action.command) return null;
  return {
    language: context.request.language,
    command: action.command,
    clientCommand: action.command.startsWith("editor.action.")
      ? action.command
      : undefined,
    arguments: [...(action.commandArguments ?? [])],
    filePath: context.request.filePath,
    line: context.request.line,
    column: context.request.column,
  };
}

function triggerClientCodeActionCommand(
  monaco: typeof import("monaco-editor"),
  payload: CodeLensExecutionPayload,
): boolean {
  if (!payload.clientCommand) return false;
  const normalizedPath = payload.filePath.replace(/\\/g, "/");
  if (typeof monaco.editor.getEditors !== "function") return false;
  const editors = monaco.editor.getEditors();
  const target =
    editors.find((editor) => {
      const model = editor.getModel();
      if (!model) return false;
      try {
        const modelPath = resolveModelFilePath(model);
        return modelPath?.replace(/\\/g, "/") === normalizedPath;
      } catch {
        return false;
      }
    }) ?? editors.find((editor) => editor.hasTextFocus());
  if (!target) return false;
  target.setPosition({
    lineNumber: payload.line + 1,
    column: payload.column + 1,
  });
  target.focus();
  const commandPayload =
    payload.arguments.length === 0
      ? undefined
      : payload.arguments.length === 1
        ? payload.arguments[0]
        : [...payload.arguments];
  target.trigger(
    LSP_CODE_ACTION_COMMAND,
    payload.clientCommand,
    commandPayload,
  );
  return true;
}

/**
 * G-FEAT-02 + prompt-8 + G-COMP-02: Monaco LSP integration.
 *
 * Completion (with sortText/filterText/commitCharacters + resolveCompletionItem),
 * hover, definition, references, format, document symbols, semantic tokens.
 * Paths: always prefer openFiles[].path (absolute disk path), not model.uri
 * alone (BUG-IDE-03).
 */

function lspCompletionKindToMonaco(kind: number): number {
  switch (kind) {
    case 1: return 18; // Text
    case 2: return 0; // Method
    case 3: return 1; // Function
    case 4: return 2; // Constructor
    case 5: return 3; // Field
    case 6: return 4; // Variable
    case 7: return 5; // Class
    case 8: return 7; // Interface
    case 9: return 8; // Module
    case 10: return 9; // Property
    case 11: return 12; // Unit
    case 12: return 13; // Value
    case 13: return 15; // Enum
    case 14: return 17; // Keyword
    case 15: return 27; // Snippet
    case 16: return 19; // Color
    case 17: return 20; // File
    case 18: return 21; // Reference
    case 19: return 23; // Folder
    case 20: return 16; // EnumMember
    case 21: return 14; // Constant
    case 22: return 6; // Struct
    case 23: return 10; // Event
    case 24: return 11; // Operator
    case 25: return 24; // TypeParameter
    default: return 18; // Text
  }
}

function lspSymbolKindToMonaco(kind: number): number {
  // LSP SymbolKind is 1-based while Monaco's matching enum is 0-based.
  return Number.isInteger(kind) && kind >= 1 && kind <= 26 ? kind - 1 : 12;
}

/**
 * Resolve absolute disk path for an editor model (prompt-8 Task 8-C).
 * Prefer active/open file path matching the model; fall back to URI path.
 */
export function resolveModelFilePath(
  model: monacoEditor.editor.ITextModel,
  _preferredPath?: string,
): string | null {
  const uriText = model.uri.toString();
  const uriPath = model.uri.path || uriText;
  const normalizedURI = uriPath.replace(/\\/g, "/");
  const isInMemory =
    uriText.startsWith("inmemory:") ||
    uriText.startsWith("untitled:") ||
    uriPath.startsWith("inmemory:") ||
    uriPath.startsWith("untitled:");

  // 真实 URI 最可靠，优先匹配已打开文件。
  const uriOpenFile = !isInMemory
    ? editorState.openFiles.find((file) => {
        if (!file.path) return false;
        const normalizedPath = file.path.replace(/\\/g, "/");
        return (
          normalizedPath === normalizedURI ||
          normalizedURI.endsWith(normalizedPath) ||
          normalizedPath.endsWith(normalizedURI.replace(/^\//, ""))
        );
      })
    : undefined;
  if (uriOpenFile?.path) return uriOpenFile.path;
  if (!isInMemory) {
    if (/^\/[A-Za-z]:\//.test(uriPath)) {
      return uriPath.slice(1).replace(/\//g, "\\");
    }
    return uriPath;
  }

  // Vue Monaco Editor 未传 path 时复用 in-memory model。切换标签页后，
  // 注册时捕获的 preferredPath 会过期，因此用当前 buffer 内容反查文件。
  if (model.isDisposed?.()) return null;
  const content = model.getValue();
  const contentMatches = editorState.openFiles.filter(
    (file) => file.path && file.content === content,
  );
  if (contentMatches.length === 1) return contentMatches[0].path;
  return null;
}

/** Map Monaco language id (+ path) to LSP language key. */
export function monacoLangToLSPKey(
  monacoLang: string,
  filePath: string,
): string | null {
  const lower = filePath.toLowerCase();
  if (lower.endsWith(".vue")) return "vue";
  if (
    monacoLang === "go-template" ||
    lower.endsWith(".gohtml") ||
    lower.endsWith(".tmpl") ||
    lower.endsWith(".gotmpl")
  ) {
    return "go";
  }
  if (monacoLang === "go" || lower.endsWith(".go")) return "go";
  if (
    monacoLang === "typescript" ||
    monacoLang === "typescriptreact" ||
    lower.endsWith(".ts") ||
    lower.endsWith(".tsx")
  ) {
    return "typescript";
  }
  if (
    monacoLang === "javascript" ||
    monacoLang === "javascriptreact" ||
    lower.endsWith(".js") ||
    lower.endsWith(".jsx")
  ) {
    return "javascript";
  }
  if (
    monacoLang === "python" ||
    lower.endsWith(".py") ||
    lower.endsWith(".pyw")
  ) {
    return "python";
  }
  if (monacoLang === "rust" || lower.endsWith(".rs")) {
    return "rust";
  }
  if (
    monacoLang === "json" ||
    monacoLang === "jsonc" ||
    lower.endsWith(".json") ||
    lower.endsWith(".jsonc")
  ) {
    return "json";
  }
  if (
    monacoLang === "css" ||
    monacoLang === "scss" ||
    monacoLang === "less" ||
    lower.endsWith(".css") ||
    lower.endsWith(".scss") ||
    lower.endsWith(".less")
  ) {
    return "css";
  }
  if (
    monacoLang === "html" ||
    lower.endsWith(".html") ||
    lower.endsWith(".htm")
  ) {
    return "html";
  }
  if (
    monacoLang === "yaml" ||
    monacoLang === "yml" ||
    lower.endsWith(".yaml") ||
    lower.endsWith(".yml")
  ) {
    return "yaml";
  }
  return null;
}

// G-COMP-02: trigger characters for identifier-aware completion.
// Letters a-z, A-Z are NOT included because Monaco already triggers on
// word characters when quickSuggestions is enabled. We add the punctuation
// marks that meaningfully start a new completion context.
const DEFAULT_LSP_TRIGGER_CHARS = [
  ".",
  ":",
  "(",
  "<",
  "@",
  "$",
  '"',
  "'",
  "`",
  ",",
  "/",
  "\\",
];

/**
 * 服务端尚未返回 completionProvider.triggerCharacters 时使用的降级值。
 * 数组按 LSP server key 区分，避免给所有语言注册一组过宽的字符。
 */
const LSP_TRIGGER_CHARS_BY_LANGUAGE: Record<string, readonly string[]> = {
  go: [".", "("],
  typescript: [".", '"', "'", "<", "/"],
  javascript: [".", '"', "'", "<", "/"],
  vue: [".", '"', "'", "<", "/"],
  angular: [".", '"', "'", "<", "/"],
  python: ["."],
  rust: [".", ":"],
};

export function getFallbackTriggerCharacters(language: string): string[] {
  const lspLanguage = monacoLangToLSPKey(language, "") ?? language;
  return [
    ...(LSP_TRIGGER_CHARS_BY_LANGUAGE[lspLanguage] ??
      DEFAULT_LSP_TRIGGER_CHARS),
  ];
}

/** Monaco CompletionTriggerKind -> LSP CompletionTriggerKind。 */
export function mapMonacoCompletionTriggerKind(
  triggerKind: number,
): 1 | 2 | 3 {
  switch (triggerKind) {
    case 1:
      return 2;
    case 3:
      return 3;
    case 0:
    case 2:
    default:
      return 1;
  }
}

// G-COMP-02: characters that auto-commit a completion when typed after selecting
// an item. E.g. selecting "fmt" then typing "." commits "fmt" and inserts ".".
const LSP_COMMIT_CHARS = [".", "(", "[", ":", "=", ",", ";"];

// G-COMP-02: sort prefix for completion items. Lower = higher priority.
// Exact match → "0", prefix match → "1", substring → "2", else "3".
function computeSortText(label: string, typedWord: string): string {
  if (!typedWord) return "1";
  const lower = label.toLowerCase();
  const typed = typedWord.toLowerCase();
  if (lower === typed) return "0"; // exact match — highest priority
  if (lower.startsWith(typed)) return "1"; // prefix match
  if (lower.includes(typed)) return "2"; // substring match
  return "3";
}

// Priority 2 (prompt-1.md): Monaco CompletionItemInsertTextRule.InsertAsSnippet
// === 4. 使用字面量（与现有 insertTextRules: 0 / None 约定一致），使该纯映射
// 辅助函数不依赖运行时 monaco 导入，便于单元测试。
export const INSERT_AS_SNIPPET_RULE = 4;
export const KEEP_WHITESPACE_RULE = 1;

const DEPRECATED_COMPLETION_TAG = 1;

interface LSPBackedCompletionItem
  extends monacoEditor.languages.CompletionItem {
  /** 初始 LSP item，resolve 时必须连同 data 原样传回服务端。 */
  __lspCompletionItem?: LSPCompletionItem;
  /** 最近一次 completionItem/resolve 的完整响应。 */
  __resolvedLSPCompletionItem?: LSPCompletionItem;
  /** provide 阶段按真实文件路径解析出的 server key（例如 vue）。 */
  __lspLanguage?: string;
}

function mapCompletionDocumentation(
  documentation: LSPCompletionItem["documentation"],
): string | monacoEditor.IMarkdownString | undefined {
  if (typeof documentation === "string") {
    return documentation;
  }
  if (documentation?.value) {
    return documentation.kind === "plaintext"
      ? documentation.value
      : { value: documentation.value };
  }
  return undefined;
}

function lspRangeToMonaco(range: LSPRange): monacoEditor.IRange {
  return {
    startLineNumber: range.start.line + 1,
    startColumn: range.start.character + 1,
    endLineNumber: range.end.line + 1,
    endColumn: range.end.character + 1,
  };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function isLSPPosition(value: unknown): value is LSPRange["start"] {
  return (
    isRecord(value) &&
    typeof value.line === "number" &&
    typeof value.character === "number"
  );
}

function isLSPRange(value: unknown): value is LSPRange {
  return (
    isRecord(value) &&
    isLSPPosition(value.start) &&
    isLSPPosition(value.end)
  );
}

function mapCompletionTextEdit(
  item: LSPCompletionItem,
): {
  insertText?: string;
  range?:
    | monacoEditor.IRange
    | monacoEditor.languages.CompletionItemRanges;
} {
  const edit: unknown = item.textEdit;
  if (!isRecord(edit)) return {};

  let mappedRange:
    | monacoEditor.IRange
    | monacoEditor.languages.CompletionItemRanges
    | undefined;
  const editRange = edit.range;
  if (isLSPRange(edit.insert) && isLSPRange(edit.replace)) {
    // InsertReplaceEdit uses top-level insert/replace fields in LSP 3.17.
    mappedRange = {
      insert: lspRangeToMonaco(edit.insert),
      replace: lspRangeToMonaco(edit.replace),
    };
  } else if (
    isRecord(editRange) &&
    isLSPRange(editRange.insert) &&
    isLSPRange(editRange.replace)
  ) {
    mappedRange = {
      insert: lspRangeToMonaco(editRange.insert),
      replace: lspRangeToMonaco(editRange.replace),
    };
  } else if (isLSPRange(editRange)) {
    mappedRange = lspRangeToMonaco(editRange);
  } else if (
    typeof edit.startLine === "number" &&
    typeof edit.startCol === "number" &&
    typeof edit.endLine === "number" &&
    typeof edit.endCol === "number"
  ) {
    mappedRange = {
      startLineNumber: edit.startLine + 1,
      startColumn: edit.startCol + 1,
      endLineNumber: edit.endLine + 1,
      endColumn: edit.endCol + 1,
    };
  }
  return {
    insertText: typeof edit.newText === "string" ? edit.newText : undefined,
    range: mappedRange,
  };
}

/**
 * Priority 2 (prompt-1.md): 前端期望 LSP server 初始化时携带的客户端能力。
 * 镜像 services/lsp_service.go initializeLocked 中构造的 capabilities 对象。
 * 在此声明以便 snippetSupport 契约可从前端单测校验。
 */
export const LSP_CLIENT_CAPABILITIES = {
  textDocument: {
    completion: {
      completionItem: {
        snippetSupport: true,
      },
    },
  },
} as const;

/**
 * Priority 2 (prompt-1.md): 将单个 LSP CompletionItem 映射为 Monaco CompletionItem。
 *   - insertTextFormat === 2 (Snippet)：设置 InsertAsSnippet 规则，insertText 作为
 *     snippet 透传（Monaco 解析 $1/$2/${1:default} 占位符）。
 *   - labelDetails：detail（短签名）→ Monaco CompletionItemLabel.detail，
 *     description（完整描述）→ CompletionItemLabel.description 及 documentation 面板。
 * 抽取为纯函数以便在没有运行 Monaco/LSP 栈的情况下单元测试映射逻辑。
 */
export function mapLSPCompletionToMonaco(
  item: LSPCompletionItem,
  range: monacoEditor.IRange,
  typedWord: string,
): monacoEditor.languages.CompletionItem {
  const isSnippet = item.insertTextFormat === 2;
  const textEdit = mapCompletionTextEdit(item);

  // Monaco 0.52 CompletionItemLabel = { label, detail?, description? }。
  // 有 labelDetails 时使用对象 label（签名/描述内联展示），否则退化为纯字符串。
  const label: string | monacoEditor.languages.CompletionItemLabel =
    item.labelDetails
      ? {
          label: item.label,
          detail: item.labelDetails.detail,
          description: item.labelDetails.description,
        }
      : item.label;

  const base: LSPBackedCompletionItem = {
    label,
    kind: lspCompletionKindToMonaco(
      item.kind,
    ) as monacoEditor.languages.CompletionItemKind,
    // labelDetails.detail 已通过 label.detail 内联展示；item.detail 保留给 LSP 的
    // 独立 detail 字段（如 "func(...)"、类型信息）。
    detail: item.detail || undefined,
    insertText:
      textEdit.insertText ?? item.textEditText ?? item.insertText ?? item.label,
    insertTextRules:
      (isSnippet ? INSERT_AS_SNIPPET_RULE : 0) |
      (item.insertTextMode === 1 ? KEEP_WHITESPACE_RULE : 0),
    range: textEdit.range ?? range,
    // G-COMP-02: sort by match quality (exact > prefix > substring).
    sortText: item.sortText ?? computeSortText(item.label, typedWord),
    // G-COMP-02: use label as filter text so Monaco's fuzzy filter works.
    filterText: item.filterText ?? item.label,
    // G-COMP-02: auto-commit on these characters.
    commitCharacters: item.commitCharacters ?? LSP_COMMIT_CHARS,
    preselect: item.preselect,
    __lspCompletionItem: item,
  };
  const documentation = mapCompletionDocumentation(item.documentation);
  if (documentation !== undefined) {
    base.documentation = documentation;
  } else if (item.labelDetails?.description) {
    // labelDetails.description 承载完整描述，呈现到 documentation 面板。
    base.documentation = item.labelDetails.description;
  }
  if (item.deprecated || item.tags?.includes(1)) {
    base.tags = [
      DEPRECATED_COMPLETION_TAG as monacoEditor.languages.CompletionItemTag,
    ];
  }
  if (item.additionalEdits?.length) {
    base.additionalTextEdits = item.additionalEdits.map((e) => ({
      range: {
        startLineNumber: e.startLine + 1,
        startColumn: e.startCol + 1,
        endLineNumber: e.endLine + 1,
        endColumn: e.endCol + 1,
      },
      text: e.newText,
    }));
  }
  return base;
}

/**
 * G-COMP-02: Convert LSP document symbols (0-based) to Monaco document
 * symbols (1-based). Recursive to preserve the symbol hierarchy.
 */
function convertLSPDocumentSymbols(
  symbols: import("@/types").LSPDocumentSymbol[],
): monacoEditor.languages.DocumentSymbol[] {
  return symbols.map((s) => ({
    name: s.name,
    detail: s.detail || "",
    kind: lspSymbolKindToMonaco(s.kind) as monacoEditor.languages.SymbolKind,
    tags: [],
    range: {
      startLineNumber: s.range.start.line + 1,
      startColumn: s.range.start.character + 1,
      endLineNumber: s.range.end.line + 1,
      endColumn: s.range.end.character + 1,
    },
    selectionRange: {
      startLineNumber: s.selectionRange.start.line + 1,
      startColumn: s.selectionRange.start.character + 1,
      endLineNumber: s.selectionRange.end.line + 1,
      endColumn: s.selectionRange.end.character + 1,
    },
    children: s.children?.length
      ? convertLSPDocumentSymbols(s.children)
      : undefined,
  }));
}

/**
 * Register LSP-backed providers. Returns IDisposable for unmount cleanup.
 */
// G-HL-03: Language configurations for Go / TypeScript / JavaScript.
// Registers autoClosingPairs, surroundingPairs, brackets, comments,
// foldingRules, and onEnterRules.
function registerLanguageConfigurations(
  monaco: typeof import("monaco-editor"),
): monacoEditor.IDisposable {
  const registrations: monacoEditor.IDisposable[] = [];
  const track = (disposable: monacoEditor.IDisposable | undefined) => {
    if (disposable?.dispose) registrations.push(disposable);
  };

  // G-HL-03: Ensure typescriptreact / javascriptreact language IDs are
  // registered before setLanguageConfiguration is called. Monaco only
  // registers go/typescript/javascript by default; tsx/jsx must be added
  // explicitly or setLanguageConfiguration throws
  // "Cannot set configuration for unknown language".
  const registeredIds = new Set(
    monaco.languages.getLanguages().map((l) => l.id),
  );
  if (!registeredIds.has("typescriptreact")) {
    monaco.languages.register({
      id: "typescriptreact",
      extensions: [".tsx"],
      aliases: ["TypeScript React", "tsx"],
    });
  }
  if (!registeredIds.has("javascriptreact")) {
    monaco.languages.register({
      id: "javascriptreact",
      extensions: [".jsx"],
      aliases: ["JavaScript React", "jsx"],
    });
  }
  if (!registeredIds.has("go-template")) {
    monaco.languages.register({
      id: "go-template",
      extensions: [".gohtml", ".tmpl", ".gotmpl"],
      aliases: ["Go Template", "gotmpl"],
      mimetypes: ["text/x-go-template"],
    });
  }
  // Register the "vue" language id so setLanguageConfiguration("vue", ...)
  // below does not throw "Cannot set configuration for unknown language vue".
  // Note: .vue files are mapped to the "html" language id by detectLanguage
  // (see lib/language.ts) for syntax highlighting; we intentionally do NOT
  // claim the .vue extension here to avoid conflicting with that mapping.
  if (!registeredIds.has("vue")) {
    monaco.languages.register({
      id: "vue",
      aliases: ["Vue"],
    });
  }

  track(monaco.languages.setMonarchTokensProvider("go-template", {
    defaultToken: "",
    tokenPostfix: ".gotmpl",
    brackets: [{ open: "{{", close: "}}", token: "delimiter.curly" }],
    tokenizer: {
      root: [
        [/./, { token: "@rematch", next: "@html", nextEmbedded: "text/html" }],
      ],
      html: [
        [
          /\{\{-?/,
          { token: "delimiter.curly", next: "@template", nextEmbedded: "@pop" },
        ],
        [/./, ""],
      ],
      template: [
        [
          /-?\}\}/,
          {
            token: "delimiter.curly",
            next: "@html",
            nextEmbedded: "text/html",
          },
        ],
        [/\b(?:if|else|end|range|with|template|define|block)\b/, "keyword"],
        [/\$[A-Za-z_]\w*/, "variable"],
        [/\.[A-Za-z_]\w*/, "variable"],
        [/"(?:[^"\\]|\\.)*"/, "string"],
        [/-?\d+(?:\.\d+)?/, "number"],
        [/\|/, "operator"],
        [/[A-Za-z_]\w*/, "identifier"],
      ],
    },
  }));

  track(monaco.languages.setLanguageConfiguration("go-template", {
    comments: { blockComment: ["{{/*", "*/}}"] },
    brackets: [
      ["{{", "}}"],
      ["(", ")"],
      ["[", "]"],
    ],
    autoClosingPairs: [
      { open: "{{", close: "}}" },
      { open: "(", close: ")" },
      { open: "[", close: "]" },
      { open: '"', close: '"' },
    ],
    surroundingPairs: [
      { open: "{{", close: "}}" },
      { open: "(", close: ")" },
      { open: "[", close: "]" },
      { open: '"', close: '"' },
    ],
  } as monacoEditor.languages.LanguageConfiguration));

  // --- Go language configuration ---
  track(monaco.languages.setLanguageConfiguration("go", {
    comments: {
      lineComment: "//",
      blockComment: ["/*", "*/"],
    },
    brackets: [
      ["{", "}"],
      ["[", "]"],
      ["(", ")"],
    ],
    autoClosingPairs: [
      { open: "{", close: "}" },
      { open: "[", close: "]" },
      { open: "(", close: ")" },
      { open: "`", close: "`", notIn: ["string"] },
      { open: '"', close: '"', notIn: ["string"] },
      { open: "'", close: "'", notIn: ["string", "comment"] },
    ],
    surroundingPairs: [
      { open: "{", close: "}" },
      { open: "[", close: "]" },
      { open: "(", close: ")" },
      { open: "`", close: "`" },
      { open: '"', close: '"' },
      { open: "'", close: "'" },
    ],
    folding: {
      markers: {
        start: /^\s*\/\/\s*region\b/,
        end: /^\s*\/\/\s*endregion\b/,
      },
    },
    onEnterRules: [
      {
        beforeText: /^\s*\/\/.*$/,
        action: {
          indentAction: monaco.languages.IndentAction.None,
          appendText: "// ",
        },
      },
    ],
  } as monacoEditor.languages.LanguageConfiguration));

  // --- TypeScript / JavaScript language configuration ---
  const tsJsConfig: monacoEditor.languages.LanguageConfiguration = {
    comments: {
      lineComment: "//",
      blockComment: ["/*", "*/"],
    },
    brackets: [
      ["{", "}"],
      ["[", "]"],
      ["(", ")"],
    ],
    autoClosingPairs: [
      { open: "{", close: "}" },
      { open: "[", close: "]" },
      { open: "(", close: ")" },
      { open: "`", close: "`", notIn: ["string"] },
      { open: '"', close: '"', notIn: ["string"] },
      { open: "'", close: "'", notIn: ["string", "comment"] },
      { open: "/**", close: " */", notIn: ["string"] },
    ],
    surroundingPairs: [
      { open: "{", close: "}" },
      { open: "[", close: "]" },
      { open: "(", close: ")" },
      { open: "`", close: "`" },
      { open: '"', close: '"' },
      { open: "'", close: "'" },
    ],
    folding: {
      markers: {
        start: /^\s*\/\/\s*#?region\b/,
        end: /^\s*\/\/\s*#?endregion\b/,
      },
    },
    onEnterRules: [
      {
        beforeText: /^\s*\/\/.*$/,
        action: {
          indentAction: monaco.languages.IndentAction.None,
          appendText: "// ",
        },
      },
      {
        beforeText: /^\s*\/\*\*?(.*)$/,
        action: {
          indentAction: monaco.languages.IndentAction.IndentOutdent,
          appendText: " * ",
        },
      },
      {
        beforeText: /^\s*\*\s.*$/,
        action: {
          indentAction: monaco.languages.IndentAction.None,
          appendText: "* ",
        },
      },
      {
        beforeText: /^\s*\*\s*$/,
        afterText: /^\s*\*\/$/,
        action: { indentAction: monaco.languages.IndentAction.IndentOutdent },
      },
    ],
  } as monacoEditor.languages.LanguageConfiguration;
  track(monaco.languages.setLanguageConfiguration("typescript", tsJsConfig));
  track(monaco.languages.setLanguageConfiguration("javascript", tsJsConfig));
  track(monaco.languages.setLanguageConfiguration("typescriptreact", tsJsConfig));
  track(monaco.languages.setLanguageConfiguration("javascriptreact", tsJsConfig));

  // --- HTML language configuration ---
  // BUG4a: Monaco's default HTML autoClosingPairs does NOT include
  // { open: "<", close: ">" }, so typing "<" does not auto-insert ">",
  // and typing a tag name does not auto-close. This makes HTML editing
  // feel broken (typing "div" yields "div" instead of "<div></div>").
  // Explicitly register an HTML language configuration with the `<`/`>`
  // auto-closing pair plus standard quotes/brackets. The actual tag-name
  // → full-tag expansion (e.g. "div" → "<div></div>") is provided by the
  // Emmet completion provider (see emmet.ts).
  track(monaco.languages.setLanguageConfiguration("html", {
    comments: { blockComment: ["<!--", "-->"] },
    brackets: [
      ["<!--", "-->"],
      ["<", ">"],
      ["{", "}"],
      ["(", ")"],
    ],
    autoClosingPairs: [
      { open: "<", close: ">", notIn: ["string", "comment"] },
      { open: "'", close: "'", notIn: ["string", "comment"] },
      { open: '"', close: '"', notIn: ["string", "comment"] },
      { open: "{", close: "}", notIn: ["string", "comment"] },
      { open: "[", close: "]", notIn: ["string", "comment"] },
      { open: "(", close: ")", notIn: ["string", "comment"] },
      { open: "`", close: "`", notIn: ["string", "comment"] },
    ],
    surroundingPairs: [
      { open: "<", close: ">" },
      { open: "'", close: "'" },
      { open: '"', close: '"' },
      { open: "{", close: "}" },
      { open: "[", close: "]" },
      { open: "(", close: ")" },
      { open: "`", close: "`" },
    ],
    // autoCloseBefore: when the user types one of these chars while an
    // auto-closed pair is pending, the close marker is typed over (skipped)
    // instead of inserted before. This matches VSCode HTML behavior.
    autoCloseBefore: ";:.,=}])>` \n\t",
    onEnterRules: [
      {
        // Press Enter inside <!-- ... --> → continue comment with " * "
        beforeText: /^\s*<!--.*$/,
        afterText: /^.*-->\s*$/,
        action: { indentAction: monaco.languages.IndentAction.IndentOutdent },
      },
    ],
  } as monacoEditor.languages.LanguageConfiguration));

  // Vue uses HTML syntax for <template> blocks — share the HTML config so
  // autoClosingPairs and tag completion work inside .vue files too.
  track(monaco.languages.setLanguageConfiguration("vue", {
    comments: { blockComment: ["<!--", "-->"] },
    brackets: [
      ["<!--", "-->"],
      ["<", ">"],
      ["{", "}"],
      ["(", ")"],
    ],
    autoClosingPairs: [
      { open: "<", close: ">", notIn: ["string", "comment"] },
      { open: "'", close: "'", notIn: ["string", "comment"] },
      { open: '"', close: '"', notIn: ["string", "comment"] },
      { open: "{", close: "}", notIn: ["string", "comment"] },
      { open: "[", close: "]", notIn: ["string", "comment"] },
      { open: "(", close: ")", notIn: ["string", "comment"] },
      { open: "`", close: "`", notIn: ["string", "comment"] },
    ],
    surroundingPairs: [
      { open: "<", close: ">" },
      { open: "'", close: "'" },
      { open: '"', close: '"' },
      { open: "{", close: "}" },
      { open: "[", close: "]" },
      { open: "(", close: ")" },
      { open: "`", close: "`" },
    ],
    autoCloseBefore: ";:.,=}])>` \n\t",
  } as monacoEditor.languages.LanguageConfiguration));

  return {
    dispose() {
      for (const registration of registrations.reverse()) {
        try {
          registration.dispose();
        } catch {
          // Monaco registrations are idempotent in practice; keep cleanup robust.
        }
      }
    },
  };
}

// G-COMP-04: Common code snippets for Go, TypeScript, and JavaScript.
// These provide quick templates that work even without an LSP server.
interface SnippetDef {
  label: string;
  detail: string;
  description: string;
  insertText: string;
}
const SNIPPETS: Record<string, SnippetDef[]> = {
  go: [
    {
      label: "func",
      detail: "func declaration",
      description: "Function declaration",
      insertText: "func ${1:name}(${2:params}) ${3:returnType} {\n\t${0}\n}",
    },
    {
      label: "method",
      detail: "method on type",
      description: "Method declaration",
      insertText:
        "func (${1:receiver} ${2:*Type}) ${3:Name}(${4:params}) ${5:returnType} {\n\t${0}\n}",
    },
    {
      label: "struct",
      detail: "struct type",
      description: "Struct type declaration",
      insertText: "type ${1:Name} struct {\n\t${0}\n}",
    },
    {
      label: "interface",
      detail: "interface type",
      description: "Interface type declaration",
      insertText: "type ${1:Name} interface {\n\t${0}\n}",
    },
    {
      label: "for",
      detail: "for loop",
      description: "Basic for loop",
      insertText: "for ${1:i} := 0; ${1:i} < ${2:n}; ${1:i}++ {\n\t${0}\n}",
    },
    {
      label: "forr",
      detail: "for range loop",
      description: "For range loop",
      insertText: "for ${1:i}, ${2:v} := range ${3:collection} {\n\t${0}\n}",
    },
    {
      label: "if",
      detail: "if statement",
      description: "If statement",
      insertText: "if ${1:condition} {\n\t${0}\n}",
    },
    {
      label: "iferr",
      detail: "if err != nil",
      description: "Error check pattern",
      insertText: "if err != nil {\n\treturn ${0:err}\n}",
    },
    {
      label: "switch",
      detail: "switch statement",
      description: "Switch statement",
      insertText: "switch ${1:variable} {\ncase ${2:value}:\n\t${0}\n}",
    },
    {
      label: "select",
      detail: "select statement",
      description: "Select statement for channels",
      insertText: "select {\ncase ${1:msg} := <-${2:chan}:\n\t${0}\n}",
    },
    {
      label: "gofunc",
      detail: "goroutine anonymous func",
      description: "Goroutine with anonymous function",
      insertText: "go func() {\n\t${0}\n}()",
    },
    {
      label: "defer",
      detail: "defer statement",
      description: "Defer statement",
      insertText: "defer ${0:func}()",
    },
    {
      label: "test",
      detail: "test function",
      description: "Test function",
      insertText: "func Test${1:Name}(t *testing.T) {\n\t${0}\n}",
    },
    {
      label: "benchmark",
      detail: "benchmark function",
      description: "Benchmark function",
      insertText:
        "func Benchmark${1:Name}(b *testing.B) {\n\tfor i := 0; i < b.N; i++ {\n\t\t${0}\n\t}\n}",
    },
  ],
  typescript: [
    {
      label: "func",
      detail: "function declaration",
      description: "Function declaration",
      insertText: "function ${1:name}(${2:params}): ${3:void} {\n\t${0}\n}",
    },
    {
      label: "afunc",
      detail: "arrow function",
      description: "Arrow function",
      insertText: "const ${1:name} = (${2:params}): ${3:void} => {\n\t${0}\n}",
    },
    {
      label: "class",
      detail: "class declaration",
      description: "Class declaration",
      insertText: "class ${1:Name} {\n\t${0}\n}",
    },
    {
      label: "interface",
      detail: "interface declaration",
      description: "TypeScript interface",
      insertText: "interface ${1:Name} {\n\t${0}\n}",
    },
    {
      label: "type",
      detail: "type alias",
      description: "Type alias declaration",
      insertText: "type ${1:Name} = ${0:type}",
    },
    {
      label: "enum",
      detail: "enum declaration",
      description: "TypeScript enum",
      insertText: "enum ${1:Name} {\n\t${0}\n}",
    },
    {
      label: "for",
      detail: "for loop",
      description: "For loop",
      insertText:
        "for (let ${1:i} = 0; ${1:i} < ${2:arr}.length; ${1:i}++) {\n\t${0}\n}",
    },
    {
      label: "forof",
      detail: "for...of loop",
      description: "For...of loop",
      insertText: "for (const ${1:item} of ${2:iterable}) {\n\t${0}\n}",
    },
    {
      label: "forin",
      detail: "for...in loop",
      description: "For...in loop",
      insertText: "for (const ${1:key} in ${2:obj}) {\n\t${0}\n}",
    },
    {
      label: "if",
      detail: "if statement",
      description: "If statement",
      insertText: "if (${1:condition}) {\n\t${0}\n}",
    },
    {
      label: "switch",
      detail: "switch statement",
      description: "Switch statement",
      insertText:
        "switch (${1:variable}) {\ncase ${2:value}:\n\t${0}\n\tbreak;\n}",
    },
    {
      label: "try",
      detail: "try/catch",
      description: "Try/catch block",
      insertText: "try {\n\t${0}\n} catch (${1:err}) {\n\t${2}\n}",
    },
    {
      label: "import",
      detail: "import statement",
      description: "ESM import",
      insertText: "import ${1:name} from '${2:module}';",
    },
    {
      label: "export",
      detail: "export declaration",
      description: "ESM export",
      insertText: "export ${0:declaration}",
    },
    {
      label: "console",
      detail: "console.log",
      description: "Console log",
      insertText: "console.log(${0});",
    },
  ],
  javascript: [
    {
      label: "func",
      detail: "function declaration",
      description: "Function declaration",
      insertText: "function ${1:name}(${2:params}) {\n\t${0}\n}",
    },
    {
      label: "afunc",
      detail: "arrow function",
      description: "Arrow function",
      insertText: "const ${1:name} = (${2:params}) => {\n\t${0}\n}",
    },
    {
      label: "class",
      detail: "class declaration",
      description: "Class declaration",
      insertText: "class ${1:Name} {\n\t${0}\n}",
    },
    {
      label: "for",
      detail: "for loop",
      description: "For loop",
      insertText:
        "for (let ${1:i} = 0; ${1:i} < ${2:arr}.length; ${1:i}++) {\n\t${0}\n}",
    },
    {
      label: "forof",
      detail: "for...of loop",
      description: "For...of loop",
      insertText: "for (const ${1:item} of ${2:iterable}) {\n\t${0}\n}",
    },
    {
      label: "forin",
      detail: "for...in loop",
      description: "For...in loop",
      insertText: "for (const ${1:key} in ${2:obj}) {\n\t${0}\n}",
    },
    {
      label: "if",
      detail: "if statement",
      description: "If statement",
      insertText: "if (${1:condition}) {\n\t${0}\n}",
    },
    {
      label: "switch",
      detail: "switch statement",
      description: "Switch statement",
      insertText:
        "switch (${1:variable}) {\ncase ${2:value}:\n\t${0}\n\tbreak;\n}",
    },
    {
      label: "try",
      detail: "try/catch",
      description: "Try/catch block",
      insertText: "try {\n\t${0}\n} catch (${1:err}) {\n\t${2}\n}",
    },
    {
      label: "import",
      detail: "import statement",
      description: "ESM import",
      insertText: "import ${1:name} from '${2:module}';",
    },
    {
      label: "export",
      detail: "export declaration",
      description: "ESM export",
      insertText: "export ${0:declaration}",
    },
    {
      label: "console",
      detail: "console.log",
      description: "Console log",
      insertText: "console.log(${0});",
    },
    {
      label: "require",
      detail: "require statement",
      description: "CommonJS require",
      insertText: "const ${1:name} = require('${2:module}');",
    },
  ],
  typescriptreact: [
    {
      label: "rfc",
      detail: "React function component",
      description: "React function component",
      insertText:
        "function ${1:Component}(${2:props}: ${3:Props}) {\n\treturn (\n\t\t<div>\n\t\t\t${0}\n\t\t</div>\n\t);\n}\n\nexport default ${1:Component};",
    },
    {
      label: "useState",
      detail: "useState hook",
      description: "React useState hook",
      insertText:
        "const [${1:state}, set${1/(.*)/${1:/capitalize}/}] = useState(${0:initialValue});",
    },
    {
      label: "useEffect",
      detail: "useEffect hook",
      description: "React useEffect hook",
      insertText: "useEffect(() => {\n\t${0}\n}, []);",
    },
    {
      label: "useCallback",
      detail: "useCallback hook",
      description: "React useCallback hook",
      insertText:
        "const ${1:callback} = useCallback((${2:args}) => {\n\t${0}\n}, [${3:deps}]);",
    },
    {
      label: "useMemo",
      detail: "useMemo hook",
      description: "React useMemo hook",
      insertText:
        "const ${1:value} = useMemo(() => {\n\treturn ${0};\n}, [${2:deps}]);",
    },
  ],
  javascriptreact: [
    {
      label: "rfc",
      detail: "React function component",
      description: "React function component",
      insertText:
        "function ${1:Component}(${2:props}) {\n\treturn (\n\t\t<div>\n\t\t\t${0}\n\t\t</div>\n\t);\n}\n\nexport default ${1:Component};",
    },
    {
      label: "useState",
      detail: "useState hook",
      description: "React useState hook",
      insertText:
        "const [${1:state}, set${1/(.*)/${1:/capitalize}/}] = useState(${0:initialValue});",
    },
    {
      label: "useEffect",
      detail: "useEffect hook",
      description: "React useEffect hook",
      insertText: "useEffect(() => {\n\t${0}\n}, []);",
    },
    {
      label: "useCallback",
      detail: "useCallback hook",
      description: "React useCallback hook",
      insertText:
        "const ${1:callback} = useCallback((${2:args}) => {\n\t${0}\n}, [${3:deps}]);",
    },
    {
      label: "useMemo",
      detail: "useMemo hook",
      description: "React useMemo hook",
      insertText:
        "const ${1:value} = useMemo(() => {\n\treturn ${0};\n}, [${2:deps}]);",
    },
  ],
};

const GO_STRUCT_TAG_KEYS = ["json", "yaml", "xml", "db"] as const;
const GO_DECLARATION_KEYWORDS = new Set([
  "break",
  "case",
  "chan",
  "const",
  "continue",
  "default",
  "defer",
  "else",
  "fallthrough",
  "for",
  "func",
  "go",
  "goto",
  "if",
  "import",
  "interface",
  "map",
  "package",
  "range",
  "return",
  "select",
  "struct",
  "switch",
  "type",
  "var",
]);

interface GoStructTagSuggestion {
  label: string;
  insertText: string;
  detail: string;
}

function goFieldNameToTagName(fieldName: string): string {
  return fieldName
    .replace(/([A-Z]+)([A-Z][a-z])/g, "$1_$2")
    .replace(/([a-z\d])([A-Z])/g, "$1_$2")
    .toLowerCase();
}

/**
 * Lightweight current-line trigger for struct tags. gopls remains the primary
 * completion source; this deliberately does not parse a Go file or infer AST.
 */
function getGoStructTagSuggestions(
  line: string,
  column: number,
): GoStructTagSuggestion[] {
  const cursorOffset = Math.max(0, Math.min(line.length, column - 1));
  const prefix = line.slice(0, cursorOffset);
  if (prefix.includes("//")) return [];

  const fieldMatch = /^\s*([A-Za-z_]\w*)\s+(.+)$/.exec(prefix);
  if (!fieldMatch || GO_DECLARATION_KEYWORDS.has(fieldMatch[1])) return [];
  const fieldType = fieldMatch[2].trimStart();
  if (!fieldType || /^(?::=|=|\(|\{|:)/.test(fieldType)) return [];

  const fieldName = goFieldNameToTagName(fieldMatch[1]);
  const backtickStart = line.indexOf("`");
  if (backtickStart < 0) {
    return GO_STRUCT_TAG_KEYS.map((key) => ({
      label: key,
      insertText: `\`${key}:"\${1:${fieldName}}\${2:,omitempty}"\``,
      detail: `${key} struct tag`,
    }));
  }

  const backtickEnd = line.indexOf("`", backtickStart + 1);
  if (
    cursorOffset <= backtickStart ||
    (backtickEnd >= 0 && cursorOffset > backtickEnd)
  ) {
    return [];
  }

  const tagEnd = backtickEnd >= 0 ? backtickEnd : line.length;
  const tagBody = line.slice(backtickStart + 1, tagEnd);
  const tagPrefix = line.slice(backtickStart + 1, cursorOffset);
  const activeValue = /(?:^|\s)(?:json|yaml|xml|db):"([^"]*)$/.exec(tagPrefix);
  if (activeValue) {
    if (activeValue[1].split(",").includes("omitempty")) return [];
    return [
      {
        label: "omitempty",
        insertText: ",omitempty",
        detail: "omit empty values",
      },
    ];
  }

  const existingKeys = new Set(
    Array.from(
      tagBody.matchAll(/(?:^|\s)(json|yaml|xml|db)\s*:/g),
      (match) => match[1],
    ),
  );
  const separator =
    tagPrefix.trim().length > 0 && !/\s$/.test(tagPrefix) ? " " : "";
  return GO_STRUCT_TAG_KEYS.filter((key) => !existingKeys.has(key)).map(
    (key) => ({
      label: key,
      insertText: `${separator}${key}:"\${1:${fieldName}}\${2:,omitempty}"`,
      detail: `${key} struct tag`,
    }),
  );
}

/**
 * M-22: Window of lines (above and below the cursor) that the document
 * highlight provider scans for word occurrences. The Monaco editor viewport
 * is not directly accessible from provideDocumentHighlights (only the model
 * and position are passed), so we fall back to a bounded window around the
 * cursor instead of scanning the whole document. 2000 lines each way covers
 * ~4000 lines — enough for typical symbol-usage locality while keeping
 * 100k-line files responsive (previously findMatches scanned every line).
 */
export const HIGHLIGHT_SCAN_WINDOW = 2000;

/**
 * M-22: Compute the bounded IRange to scan for document highlights.
 * Centers on the cursor line and clamps to the document bounds. For small
 * documents (<= 2*window+1 lines) the whole document is scanned, so
 * behavior is unchanged for typical files; only very large files get the
 * bounded treatment.
 */
export function computeDocumentHighlightScanRange(
  lineCount: number,
  cursorLine: number,
  windowLines: number,
): {
  startLineNumber: number;
  startColumn: number;
  endLineNumber: number;
  endColumn: number;
} {
  const startLineNumber = Math.max(1, cursorLine - windowLines);
  const endLineNumber = Math.min(
    Math.max(1, lineCount),
    cursorLine + windowLines,
  );
  return {
    startLineNumber,
    startColumn: 1,
    endLineNumber,
    // A very large endColumn so the search covers each line fully; Monaco
    // clamps the column to the actual line length during the scan.
    endColumn: Number.MAX_SAFE_INTEGER,
  };
}

interface ProviderLifecycle {
  active: boolean;
  triggerGenerations: Map<string, number>;
  triggerLoaded: Set<string>;
  triggerCharactersByServer: Map<string, string[]>;
}

async function loadServerTriggerCharacters(
  registrationLanguage: string,
  serverLanguage: string,
  target: string[],
  lifecycle: ProviderLifecycle,
  loader: (language: string) => Promise<string[] | null>,
): Promise<void> {
  const capabilityKey = `${registrationLanguage}\u0000${serverLanguage}`;
  const generation =
    (lifecycle.triggerGenerations.get(capabilityKey) ?? 0) + 1;
  lifecycle.triggerGenerations.set(capabilityKey, generation);
  try {
    const characters = await loader(serverLanguage);
    if (
      !lifecycle.active ||
      lifecycle.triggerGenerations.get(capabilityKey) !== generation ||
      !characters?.length
    ) {
      return;
    }
    const uniqueCharacters = [...new Set(characters.filter(Boolean))];
    if (uniqueCharacters.length > 0) {
      lifecycle.triggerCharactersByServer.set(capabilityKey, uniqueCharacters);
      const prefix = `${registrationLanguage}\u0000`;
      const mergedCharacters = [
        ...new Set(
          [...lifecycle.triggerCharactersByServer.entries()]
            .filter(([key]) => key.startsWith(prefix))
            .flatMap(([, values]) => values),
        ),
      ];
      // Monaco 保存 provider 对象中的数组引用；原地替换可让异步能力结果
      // 生效，同时兼容同一 html provider 背后的 HTML/Vue server。
      target.splice(0, target.length, ...mergedCharacters);
      lifecycle.triggerLoaded.add(capabilityKey);
    }
  } catch {
    // 旧后端尚未提供该 RPC 时继续使用按语言区分的 fallback。
  }
}

function hasCompletionDocumentation(
  documentation: monacoEditor.languages.CompletionItem["documentation"],
): boolean {
  if (typeof documentation === "string") return documentation.length > 0;
  return Boolean(documentation?.value);
}

function hasLSPCompletionDocumentation(
  documentation: LSPCompletionItem["documentation"],
): boolean {
  if (typeof documentation === "string") return documentation.length > 0;
  return Boolean(documentation?.value);
}

interface LSPModelSnapshot {
  filePath: string;
  lspLanguage: string;
  content: string;
}

function captureLSPModelSnapshot(
  model: monacoEditor.editor.ITextModel,
  registrationLanguage: string,
  preferredPath?: string,
): LSPModelSnapshot | null {
  try {
    if (model.isDisposed?.()) return null;
    const filePath = resolveModelFilePath(model, preferredPath);
    if (!filePath) return null;
    const lspLanguage = monacoLangToLSPKey(registrationLanguage, filePath);
    if (!lspLanguage || model.isDisposed?.()) return null;
    const content = model.getValue();
    if (model.isDisposed?.()) return null;
    return { filePath, lspLanguage, content };
  } catch {
    // Monaco can dispose a model between the guard and getValue().
    return null;
  }
}

function completionItemForResolve(
  item: monacoEditor.languages.CompletionItem,
): LSPCompletionItem {
  const backed = item as LSPBackedCompletionItem;
  if (backed.__resolvedLSPCompletionItem) {
    return backed.__resolvedLSPCompletionItem;
  }
  if (backed.__lspCompletionItem) return backed.__lspCompletionItem;
  return {
    label: typeof item.label === "string" ? item.label : item.label.label,
    kind: (item.kind as number) ?? 0,
    detail: item.detail || "",
    insertText: item.insertText || "",
    documentation:
      typeof item.documentation === "string"
        ? item.documentation
        : item.documentation?.value,
  };
}

function applyResolvedCompletionItem(
  item: monacoEditor.languages.CompletionItem,
  resolved: LSPCompletionItem,
): void {
  const backed = item as LSPBackedCompletionItem;
  const previous = backed.__lspCompletionItem ?? completionItemForResolve(item);
  const previousLabelDetails = previous.labelDetails ?? undefined;
  const resolvedLabelDetails = resolved.labelDetails ?? undefined;
  const labelDetails =
    previousLabelDetails || resolvedLabelDetails
      ? { ...previousLabelDetails, ...resolvedLabelDetails }
      : undefined;
  const merged: LSPCompletionItem = {
    ...previous,
    ...resolved,
    insertText: resolved.insertText ?? previous.insertText,
    textEditText: resolved.textEditText ?? previous.textEditText,
    sortText: resolved.sortText ?? previous.sortText,
    filterText: resolved.filterText ?? previous.filterText,
    commitCharacters:
      resolved.commitCharacters ?? previous.commitCharacters,
    textEdit: resolved.textEdit ?? previous.textEdit,
    additionalEdits: resolved.additionalEdits ?? previous.additionalEdits,
    tags: resolved.tags ?? previous.tags,
    documentation: resolved.documentation ?? previous.documentation,
    ...(labelDetails ? { labelDetails } : {}),
  };
  backed.__resolvedLSPCompletionItem = merged;
  if (labelDetails) {
    item.label = {
      label: merged.label || (typeof item.label === "string" ? item.label : item.label.label),
      detail: labelDetails.detail,
      description: labelDetails.description,
    };
  }
  if (resolved.detail !== null && resolved.detail !== undefined) {
    item.detail = resolved.detail;
  }
  const resolvedTextEdit = mapCompletionTextEdit(resolved);
  if (resolved.textEdit !== null && resolved.textEdit !== undefined) {
    if (resolvedTextEdit.range !== undefined) item.range = resolvedTextEdit.range;
  }
  const resolvedInsertText =
    resolvedTextEdit.insertText ??
    resolved.textEditText ??
    resolved.insertText;
  if (resolvedInsertText !== null && resolvedInsertText !== undefined) {
    item.insertText = resolvedInsertText;
  }
  const documentation = mapCompletionDocumentation(resolved.documentation);
  if (documentation !== undefined) {
    item.documentation = documentation;
  } else if (resolved.labelDetails?.description) {
    item.documentation = resolved.labelDetails.description;
  }
  if (resolved.sortText !== null && resolved.sortText !== undefined) {
    item.sortText = resolved.sortText;
  }
  if (resolved.filterText !== null && resolved.filterText !== undefined) {
    item.filterText = resolved.filterText;
  }
  if (resolved.preselect !== undefined) item.preselect = resolved.preselect;
  if (
    resolved.commitCharacters !== null &&
    resolved.commitCharacters !== undefined
  ) {
    item.commitCharacters = resolved.commitCharacters;
  }
  if (resolved.deprecated !== undefined || resolved.tags != null) {
    item.tags =
      resolved.deprecated || resolved.tags?.includes(1)
        ? [
            DEPRECATED_COMPLETION_TAG as monacoEditor.languages.CompletionItemTag,
          ]
        : [];
  }
  if (resolved.additionalEdits != null) {
    item.additionalTextEdits = resolved.additionalEdits.map((edit) => ({
      range: {
        startLineNumber: edit.startLine + 1,
        startColumn: edit.startCol + 1,
        endLineNumber: edit.endLine + 1,
        endColumn: edit.endCol + 1,
      },
      text: edit.newText,
    }));
  }
}

export const LSP_PROVIDER_LANGUAGES = [
  "go",
  "typescript",
  "javascript",
  "typescriptreact",
  "javascriptreact",
  "json",
  "css",
  "scss",
  "less",
  "html",
  "yaml",
  "go-template",
  "python",
  "rust",
] as const;

export interface LSPProviderRegistrationOptions {
  getTriggerCharacters?: (language: string) => Promise<string[] | null>;
}

interface SharedLSPProviderRegistration {
  references: number;
  disposable: monacoEditor.IDisposable;
}

const sharedLSPProviderRegistrations = new WeakMap<
  typeof import("monaco-editor"),
  SharedLSPProviderRegistration
>();

function createProviderRegistrationLease(
  monaco: typeof import("monaco-editor"),
  registration: SharedLSPProviderRegistration,
): monacoEditor.IDisposable {
  let released = false;
  return {
    dispose() {
      if (released) return;
      released = true;
      registration.references -= 1;
      if (registration.references === 0) {
        registration.disposable.dispose();
        sharedLSPProviderRegistrations.delete(monaco);
      }
    },
  };
}

export function registerLSPProviders(
  monaco: typeof import("monaco-editor"),
  preferredPath?: string,
  options: LSPProviderRegistrationOptions = {},
): monacoEditor.IDisposable {
  // Monaco 0.52.2 has no languages.registerDiagnosticsProvider API. Pull
  // diagnostics therefore remain owned by stores/lsp.ts, which polls the LSP
  // service and updates the Problems/marker state instead of inventing an API.
  const existingRegistration = sharedLSPProviderRegistrations.get(monaco);
  if (existingRegistration) {
    existingRegistration.references += 1;
    return createProviderRegistrationLease(monaco, existingRegistration);
  }

  const disposables: monacoEditor.IDisposable[] = [];
  const lifecycle: ProviderLifecycle = {
    active: true,
    triggerGenerations: new Map(),
    triggerLoaded: new Set(),
    triggerCharactersByServer: new Map(),
  };
  const completionGenerations = new WeakMap<
    monacoEditor.editor.ITextModel,
    number
  >();
  const resolveGenerations = new WeakMap<object, number>();
  const resolvePromises = new WeakMap<
    object,
    Promise<monacoEditor.languages.CompletionItem>
  >();
  const codeActionSources = new WeakMap<object, CodeActionResolutionContext>();
  const codeActionRequestGenerations = new WeakMap<
    monacoEditor.editor.ITextModel,
    number
  >();
  const codeActionResolveGenerations = new WeakMap<object, number>();
  const codeActionResolvePromises = new WeakMap<
    object,
    Promise<LSPCodeAction>
  >();
  if (monaco.editor.registerCommand) {
    disposables.push(
      monaco.editor.registerCommand(
        LSP_REFACTOR_PREVIEW_COMMAND,
        (_accessor, payload: RefactorPreviewCommandPayload) => {
          void import("@/stores/refactor").then(
            ({ openRefactorActionPreview }) => {
              openRefactorActionPreview(payload.request, payload.action);
            },
          );
        },
      ),
    );
    disposables.push(
      monaco.editor.registerCommand(
        LSP_CODE_ACTION_COMMAND,
        (_accessor, payload: CodeLensExecutionPayload) => {
          if (!payload?.language || !payload.command) return;
          if (triggerClientCodeActionCommand(monaco, payload)) return;
          void executeCodeLensCommand(payload).catch((error: unknown) => {
            console.debug("[LSP] code action command failed", error);
          });
        },
      ),
    );
  }

  const mapCodeAction = (
    source: LSPCodeAction,
    context: CodeActionResolutionContext,
  ): monacoEditor.languages.CodeAction => {
    const action: monacoEditor.languages.CodeAction = {
      title: source.title,
      kind: source.kind,
      isPreferred: source.isPreferred,
      disabled: source.disabled
        ? source.disabledReason || source.title
        : undefined,
      diagnostics: context.diagnostics,
      ranges: [context.range],
    };
    const refactor = source.kind?.startsWith("refactor") && !source.disabled;
    if (refactor && (source.preview?.files.length || source.command)) {
      action.command = {
        id: LSP_REFACTOR_PREVIEW_COMMAND,
        title: source.title,
        tooltip: source.tooltip,
        arguments: [
          {
            request: context.request,
            action: source,
          } satisfies RefactorPreviewCommandPayload,
        ],
      };
    } else if (!source.disabled) {
      const edit = codeActionWorkspaceEdit(monaco, source);
      if (edit) action.edit = edit;
      const execution = codeActionExecutionPayload(context, source);
      if (execution) {
        action.command = {
          id: LSP_CODE_ACTION_COMMAND,
          title: source.commandTitle || source.title,
          tooltip: source.tooltip,
          arguments: [execution],
        };
      }
    }
    codeActionSources.set(action, { ...context, source });
    return action;
  };

  // G-HL-03: Register language configurations (autoClosingPairs, foldingRules, etc.)
  disposables.push(registerLanguageConfigurations(monaco));
  const languages = LSP_PROVIDER_LANGUAGES;

  for (const lang of languages) {
    const triggerCharacters = getFallbackTriggerCharacters(lang);
    const triggerLanguage = monacoLangToLSPKey(lang, "") ?? lang;
    void loadServerTriggerCharacters(
      lang,
      triggerLanguage,
      triggerCharacters,
      lifecycle,
      options.getTriggerCharacters ?? GetTriggerCharacters,
    );
    disposables.push(
      monaco.languages.registerCompletionItemProvider(lang, {
        triggerCharacters,
        // G-COMP-02: resolve documentation/detail/additionalTextEdits lazily
        // on item selection. The LSP server may fill in richer documentation
        // and auto-import edits that were not present in the initial list.
        async resolveCompletionItem(item, token) {
          const backed = item as LSPBackedCompletionItem;
          if (backed.__resolvedLSPCompletionItem) return item;
          // documentation 为空就允许 server 补全；detail 已存在不应阻止 resolve。
          if (
            backed.__lspCompletionItem
              ? hasLSPCompletionDocumentation(
                  backed.__lspCompletionItem.documentation,
                )
              : hasCompletionDocumentation(item.documentation)
          ) {
            return item;
          }
          const pending = resolvePromises.get(item);
          if (pending) return pending;
          const task = (async () => {
            const generation = (resolveGenerations.get(item) ?? 0) + 1;
            resolveGenerations.set(item, generation);
            try {
              const lspLang =
                backed.__lspLanguage ?? monacoLangToLSPKey(lang, "");
              if (!lspLang) return item;
              const resolved = await resolveLSPCompletionItem(
                lspLang,
                completionItemForResolve(item),
              );
              if (
                lifecycle.active &&
                !token.isCancellationRequested &&
                resolveGenerations.get(item) === generation
              ) {
                applyResolvedCompletionItem(item, resolved);
              }
            } catch {
              // best-effort — return original if resolution fails
            }
            return item;
          })();
          resolvePromises.set(item, task);
          try {
            return await task;
          } finally {
            if (resolvePromises.get(item) === task) {
              resolvePromises.delete(item);
            }
          }
        },
        provideCompletionItems: async (model, position, context, token) => {
          const filePath = resolveModelFilePath(model, preferredPath);
          if (!filePath) return { suggestions: [] };
          const lspLang = monacoLangToLSPKey(lang, filePath);
          if (!lspLang) return { suggestions: [] };
          const generation = (completionGenerations.get(model) ?? 0) + 1;
          completionGenerations.set(model, generation);
          const line = position.lineNumber - 1;
          const column = position.column - 1;
          const content = model.getValue();
          const word = model.getWordUntilPosition(position);
          const range = {
            startLineNumber: position.lineNumber,
            endLineNumber: position.lineNumber,
            startColumn: word.startColumn,
            endColumn: word.endColumn,
          };
          const typedWord = word.word || "";
          // G-COMP-03: fetch LSP completions and auto-import candidates in
          // parallel. Auto-import provides cross-file symbol suggestions whose
          // additionalTextEdits insert the import at the top of the file.
          const [list, autoImports] = await Promise.all([
            getLSPCompletions(
              lspLang,
              filePath,
              line,
              column,
              content,
              mapMonacoCompletionTriggerKind(context.triggerKind as number),
              context.triggerCharacter,
            ),
            getAutoImportCompletions(typedWord, filePath, content, lang),
          ]);
          // completion 请求会懒启动 server；若注册阶段尚未拿到 capability，
          // 此时再刷新一次真正的 server-advertised triggerCharacters。
          if (!lifecycle.triggerLoaded.has(`${lang}\u0000${lspLang}`)) {
            void loadServerTriggerCharacters(
              lang,
              lspLang,
              triggerCharacters,
              lifecycle,
              options.getTriggerCharacters ?? GetTriggerCharacters,
            );
          }
          if (
            !lifecycle.active ||
            token.isCancellationRequested ||
            completionGenerations.get(model) !== generation
          ) {
            return { suggestions: [], incomplete: list.isIncomplete };
          }
          if (list.items.length === 0 && autoImports.length === 0) {
            return { suggestions: [], incomplete: list.isIncomplete };
          }
          // prompt-10 10-I: map additionalTextEdits (auto-import) onto Monaco
          // G-COMP-02: add sortText, filterText, commitCharacters for better UX.
          // Priority 2 (prompt-1.md): mapLSPCompletionToMonaco handles
          // insertTextFormat === 2 (Snippet) and labelDetails (detail/description).
          const suggestions: monacoEditor.languages.CompletionItem[] =
            list.items.map((item) => {
              const suggestion = mapLSPCompletionToMonaco(
                item,
                range,
                typedWord,
              ) as LSPBackedCompletionItem;
              suggestion.__lspLanguage = lspLang;
              return suggestion;
            });
          // G-COMP-03: merge auto-import suggestions (deduped by label).
          const merged = mergeAutoImportSuggestions(
            suggestions,
            autoImports,
            range,
          );
          return { suggestions: merged, incomplete: list.isIncomplete };
        },
      }),
    );

    disposables.push(
      monaco.languages.registerHoverProvider(lang, {
        provideHover: async (model, position) => {
          const snapshot = captureLSPModelSnapshot(model, lang, preferredPath);
          if (!snapshot) return null;
          try {
            const hover = await getLSPHover(
              snapshot.lspLanguage,
              snapshot.filePath,
              position.lineNumber - 1,
              position.column - 1,
              snapshot.content,
            );
            if (model.isDisposed?.() || !hover) return null;
            return {
              range: {
                startLineNumber: position.lineNumber,
                endLineNumber: position.lineNumber,
                startColumn: position.column,
                endColumn: position.column,
              },
              contents: [{ value: hover }],
            };
          } catch {
            return null;
          }
        },
      }),
    );

    // prompt-8 Task 8-F: Go to Definition
    disposables.push(
      monaco.languages.registerDefinitionProvider(lang, {
        provideDefinition: async (model, position) => {
          const snapshot = captureLSPModelSnapshot(model, lang, preferredPath);
          if (!snapshot) return null;
          try {
            const locs = await getLSPDefinition(
              snapshot.lspLanguage,
              snapshot.filePath,
              position.lineNumber - 1,
              position.column - 1,
              snapshot.content,
            );
            if (model.isDisposed?.() || !locs.length) return null;
            return locs.map((loc) => ({
              uri: monaco.Uri.file(loc.filePath),
              range: {
                startLineNumber: loc.line + 1,
                startColumn: loc.column + 1,
                endLineNumber: (loc.endLine ?? loc.line) + 1,
                endColumn: (loc.endColumn ?? loc.column) + 1,
              },
            }));
          } catch {
            return null;
          }
        },
      }),
    );

    // prompt-8 Task 8-F: Find References
    disposables.push(
      monaco.languages.registerReferenceProvider(lang, {
        provideReferences: async (model, position) => {
          const snapshot = captureLSPModelSnapshot(model, lang, preferredPath);
          if (!snapshot) return [];
          try {
            const locs = await getLSPReferences(
              snapshot.lspLanguage,
              snapshot.filePath,
              position.lineNumber - 1,
              position.column - 1,
              snapshot.content,
            );
            if (model.isDisposed?.() || !locs.length) return [];
            return locs.map((loc) => ({
              uri: monaco.Uri.file(loc.filePath),
              range: {
                startLineNumber: loc.line + 1,
                startColumn: loc.column + 1,
                endLineNumber: (loc.endLine ?? loc.line) + 1,
                endColumn: (loc.endColumn ?? loc.column) + 1,
              },
            }));
          } catch {
            return [];
          }
        },
      }),
    );

    // G-ACT-02: Go to Implementation provider — jumps to concrete implementations
    // of an interface method. Same pattern as Go to Definition.
    disposables.push(
      monaco.languages.registerImplementationProvider(lang, {
        provideImplementation: async (model, position) => {
          const snapshot = captureLSPModelSnapshot(model, lang, preferredPath);
          if (!snapshot) return null;
          try {
            const { getLSPImplementation } = await import("@/stores/lsp");
            if (model.isDisposed?.()) return null;
            const locs = await getLSPImplementation(
              snapshot.lspLanguage,
              snapshot.filePath,
              position.lineNumber - 1,
              position.column - 1,
              snapshot.content,
            );
            if (model.isDisposed?.() || !locs.length) return null;
            return locs.map((loc) => ({
              uri: monaco.Uri.file(loc.filePath),
              range: {
                startLineNumber: loc.line + 1,
                startColumn: loc.column + 1,
                endLineNumber: (loc.endLine ?? loc.line) + 1,
                endColumn: (loc.endColumn ?? loc.column) + 1,
              },
            }));
          } catch {
            return null;
          }
        },
      }),
    );

    // G-ACT-01: Code Action provider — powers the lightbulb (quick fixes,
    // refactors like extract method/variable, organize imports). When the
    // user selects an action, its workspace edits are applied to the buffers.
    disposables.push(
      monaco.languages.registerCodeActionProvider(lang, {
        provideCodeActions: async (model, range, actionContext, token) => {
          const empty = { actions: [], dispose: () => undefined };
          const generation =
            (codeActionRequestGenerations.get(model) ?? 0) + 1;
          codeActionRequestGenerations.set(model, generation);
          try {
            if (
              !lifecycle.active ||
              token?.isCancellationRequested ||
              model.isDisposed?.()
            ) {
              return empty;
            }
            const filePath = resolveModelFilePath(model, preferredPath);
            if (!filePath) return empty;
            const lspLang = monacoLangToLSPKey(lang, filePath);
            if (!lspLang) return empty;
            const content = model.getValue();
            const modelVersionId = model.getVersionId?.() ?? 0;
            const request: LSPCompletionRequest = {
              language: lspLang,
              filePath,
              line: range.startLineNumber - 1,
              column: range.startColumn - 1,
              endLine: range.endLineNumber - 1,
              endColumn: range.endColumn - 1,
              content,
            };
            const { getLSPCodeActions } = await import("@/stores/lsp");
            if (
              !lifecycle.active ||
              token?.isCancellationRequested ||
              model.isDisposed?.()
            ) {
              return empty;
            }
            const actions = await getLSPCodeActions(
              request.language,
              request.filePath,
              request.line,
              request.column,
              request.content,
              {
                endLine: request.endLine,
                endColumn: request.endColumn,
              },
            );
            if (
              !lifecycle.active ||
              token?.isCancellationRequested ||
              model.isDisposed?.() ||
              codeActionRequestGenerations.get(model) !== generation ||
              (model.getVersionId?.() ?? 0) !== modelVersionId
            ) {
              return empty;
            }
            const context: Omit<CodeActionResolutionContext, "source"> = {
              request,
              range: {
                startLineNumber: range.startLineNumber,
                startColumn: range.startColumn,
                endLineNumber: range.endLineNumber,
                endColumn: range.endColumn,
              },
              diagnostics: [...(actionContext?.markers ?? [])],
            };
            return {
              actions: actions.map((source) =>
                mapCodeAction(source, { ...context, source }),
              ),
              dispose: () => undefined,
            };
          } catch {
            return empty;
          }
        },
        resolveCodeAction: async (codeAction, token) => {
          const context = codeActionSources.get(codeAction);
          if (
            !context ||
            !lifecycle.active ||
            token?.isCancellationRequested ||
            !needsCodeActionResolve(context.source)
          ) {
            return codeAction;
          }
          const generation =
            (codeActionResolveGenerations.get(codeAction) ?? 0) + 1;
          codeActionResolveGenerations.set(codeAction, generation);
          let task = codeActionResolvePromises.get(codeAction);
          if (!task) {
            task = import("@/stores/lsp").then(({ resolveLSPCodeAction }) =>
              resolveLSPCodeAction(context.request, context.source),
            );
            codeActionResolvePromises.set(codeAction, task);
          }
          try {
            const resolved = await task;
            if (
              !lifecycle.active ||
              token?.isCancellationRequested ||
              codeActionResolveGenerations.get(codeAction) !== generation ||
              !hasResolvedCodeActionPayload(resolved)
            ) {
              return codeAction;
            }
            return mapCodeAction(resolved, { ...context, source: resolved });
          } catch {
            return codeAction;
          } finally {
            if (codeActionResolvePromises.get(codeAction) === task) {
              codeActionResolvePromises.delete(codeAction);
            }
          }
        },
      }),
    );

    // prompt-8 Task 8-G: Document formatting
    disposables.push(
      monaco.languages.registerDocumentFormattingEditProvider(lang, {
        provideDocumentFormattingEdits: async (model) => {
          const snapshot = captureLSPModelSnapshot(model, lang, preferredPath);
          if (!snapshot) return [];
          try {
            const edits = await formatLSPDocument(
              snapshot.lspLanguage,
              snapshot.filePath,
              snapshot.content,
            );
            if (model.isDisposed?.()) return [];
            return edits.map((e) => ({
              range: {
                startLineNumber: e.startLine + 1,
                startColumn: e.startCol + 1,
                endLineNumber: e.endLine + 1,
                endColumn: e.endCol + 1,
              },
              text: e.newText,
            }));
          } catch {
            return [];
          }
        },
      }),
    );

    // prompt-9 9-B + prompt-10 10-A: F2 Rename with preview confirm + apply dirty buffers
    disposables.push(
      monaco.languages.registerRenameProvider(lang, {
        provideRenameEdits: async (model, position, newName) => {
          const snapshot = captureLSPModelSnapshot(model, lang, preferredPath);
          if (!snapshot) return null;
          try {
            const { renameSymbolWorkspace } = await import("@/stores/lsp");
            if (model.isDisposed?.()) return null;
            const files = await renameSymbolWorkspace(
              snapshot.lspLanguage,
              snapshot.filePath,
              position.lineNumber - 1,
              position.column - 1,
              snapshot.content,
              newName,
            );
            if (model.isDisposed?.()) return null;
            if (!files.length) {
              return null;
            }
            // prompt-11/12: rename summary — edit counts + multi-line hunks (not single truncated line)
            const summaryLines = files.map((f) => {
              const n = f.edits?.length || 0;
              const hunks = (f.edits || []).slice(0, 4).map((e) => {
                const preview = (e.newText || "")
                  .split("\n")
                  .slice(0, 3)
                  .map((l) => l.slice(0, 80))
                  .join(" | ");
                return `    L${e.startLine + 1}-${e.endLine + 1}: ${preview || "(delete)"}`;
              });
              const more =
                (f.edits?.length || 0) > 4
                  ? `    … +${(f.edits?.length || 0) - 4} more edits`
                  : "";
              return `• ${f.filePath}  (${n} edit${n === 1 ? "" : "s"})\n${hunks.join("\n")}${more ? "\n" + more : ""}`;
            });
            const body =
              `Rename will modify ${files.length} file(s):\n\n` +
              summaryLines.slice(0, 10).join("\n\n") +
              (summaryLines.length > 10 ? "\n\n…" : "") +
              `\n\nApply → mark dirty. Save All (Ctrl+K S) writes disk. Failures → Output.`;
            try {
              const { ElMessageBox } = await import("element-plus");
              if (model.isDisposed?.()) return null;
              await ElMessageBox.confirm(body, "Rename preview", {
                type: "warning",
                confirmButtonText: "Apply",
                cancelButtonText: "Cancel",
                customClass: "rename-preview-box",
              });
              if (model.isDisposed?.()) return null;
            } catch {
              return null; // user cancelled
            }
            const { applied, failed } = await applyWorkspaceEditsDetailed(files);
            if (model.isDisposed?.()) return null;
            const { notifySuccess, notifyWarning } =
              await import("@/lib/notifications");
            if (model.isDisposed?.()) return null;
            const { pushOutput } = await import("@/stores/output");
            if (model.isDisposed?.()) return null;
            if (failed.length) {
              pushOutput(
                "LSP",
                "error",
                `rename failed:\n${failed.join("\n")}`,
              );
              notifyWarning(
                `Rename: ${applied} ok, ${failed.length} failed (see Output)`,
              );
            } else if (applied > 0) {
              notifySuccess(
                `Rename applied to ${applied} file(s) (dirty — Save All to persist)`,
              );
            } else {
              notifyWarning("Rename could not apply any file edits");
            }
            // Already applied to buffers; return empty so Monaco does not double-apply
            return { edits: [] };
          } catch {
            return null;
          }
        },
      }),
    );

    // prompt-9 9-G; G-HL-01: Signature Help with Markdown documentation.
    // Both signature-level and parameter-level documentation are rendered as
    // IMarkdownString so code blocks, links, and inline formatting display
    // correctly in the parameter hints widget.
    disposables.push(
      monaco.languages.registerSignatureHelpProvider(lang, {
        signatureHelpTriggerCharacters: ["(", ","],
        signatureHelpRetriggerCharacters: [","],
        provideSignatureHelp: async (model, position) => {
          const snapshot = captureLSPModelSnapshot(model, lang, preferredPath);
          if (!snapshot) return null;
          try {
            const { getLSPSignatureHelp } = await import("@/stores/lsp");
            if (model.isDisposed?.()) return null;
            const help = await getLSPSignatureHelp(
              snapshot.lspLanguage,
              snapshot.filePath,
              position.lineNumber - 1,
              position.column - 1,
              snapshot.content,
            );
            if (model.isDisposed?.() || !help?.label) return null;
            return {
              value: {
                signatures: [
                  {
                    label: help.label,
                    // G-HL-01: Use IMarkdownString for Markdown rendering.
                    documentation: help.documentation
                      ? { value: help.documentation }
                      : undefined,
                    parameters: (help.parameters || []).map((p) => ({
                      label: p.label,
                      // G-HL-01: Per-parameter Markdown documentation.
                      documentation: p.documentation
                        ? { value: p.documentation }
                        : undefined,
                    })),
                  },
                ],
                activeSignature: help.activeSignature ?? 0,
                activeParameter: help.activeParameter ?? 0,
              },
              dispose: () => undefined,
            };
          } catch {
            return null;
          }
        },
      }),
    );

    // G-COMP-02: Document symbol provider — powers the Outline view and
    // breadcrumbs. Converts 0-based LSP positions to 1-based Monaco positions.
    disposables.push(
      monaco.languages.registerDocumentSymbolProvider(lang, {
        provideDocumentSymbols: async (model) => {
          const snapshot = captureLSPModelSnapshot(model, lang, preferredPath);
          if (!snapshot) return [];
          try {
            const symbols = await getLSPDocumentSymbols(
              snapshot.lspLanguage,
              snapshot.filePath,
              snapshot.content,
            );
            if (model.isDisposed?.()) return [];
            return convertLSPDocumentSymbols(symbols);
          } catch {
            return [];
          }
        },
      }),
    );

    // Architecture C (prompt-1.md 498): Declaration provider — Go to
    // Declaration (textDocument/declaration). Same pattern as Definition.
    disposables.push(
      monaco.languages.registerDeclarationProvider(lang, {
        provideDeclaration: async (model, position) => {
          const snapshot = captureLSPModelSnapshot(model, lang, preferredPath);
          if (!snapshot) return null;
          try {
            const { getLSPDeclaration } = await import("@/stores/lsp");
            if (model.isDisposed?.()) return null;
            const locs = await getLSPDeclaration(
              snapshot.lspLanguage,
              snapshot.filePath,
              position.lineNumber - 1,
              position.column - 1,
              snapshot.content,
            );
            if (model.isDisposed?.() || !locs.length) return null;
            return locs.map((loc) => ({
              uri: monaco.Uri.file(loc.filePath),
              range: {
                startLineNumber: loc.line + 1,
                startColumn: loc.column + 1,
                endLineNumber: (loc.endLine ?? loc.line) + 1,
                endColumn: (loc.endColumn ?? loc.column) + 1,
              },
            }));
          } catch {
            return null;
          }
        },
      }),
    );

    // Architecture C (prompt-1.md 497): Type Definition provider — Go to Type
    // Definition (textDocument/typeDefinition). Jumps to the type definition.
    disposables.push(
      monaco.languages.registerTypeDefinitionProvider(lang, {
        provideTypeDefinition: async (model, position) => {
          const snapshot = captureLSPModelSnapshot(model, lang, preferredPath);
          if (!snapshot) return null;
          try {
            const { getLSPTypeDefinition } = await import("@/stores/lsp");
            if (model.isDisposed?.()) return null;
            const locs = await getLSPTypeDefinition(
              snapshot.lspLanguage,
              snapshot.filePath,
              position.lineNumber - 1,
              position.column - 1,
              snapshot.content,
            );
            if (model.isDisposed?.() || !locs.length) return null;
            return locs.map((loc) => ({
              uri: monaco.Uri.file(loc.filePath),
              range: {
                startLineNumber: loc.line + 1,
                startColumn: loc.column + 1,
                endLineNumber: (loc.endLine ?? loc.line) + 1,
                endColumn: (loc.endColumn ?? loc.column) + 1,
              },
            }));
          } catch {
            return null;
          }
        },
      }),
    );

    // Architecture C (prompt-1.md 498): Document Link provider — clickable
    // URLs in code (textDocument/documentLink). Monaco 的 API 名为
    // registerLinkProvider（非 registerDocumentLinkProvider）。将 0-based LSP
    // 位置映射为 1-based Monaco 位置。
    disposables.push(
      monaco.languages.registerLinkProvider(lang, {
        provideLinks: async (model) => {
          const snapshot = captureLSPModelSnapshot(model, lang, preferredPath);
          if (!snapshot) return { links: [] };
          try {
            const { getLSPDocumentLinks } = await import("@/stores/lsp");
            if (model.isDisposed?.()) return { links: [] };
            const links = await getLSPDocumentLinks(
              snapshot.lspLanguage,
              snapshot.filePath,
              snapshot.content,
            );
            if (model.isDisposed?.() || !links.length) return { links: [] };
            return {
              links: links.map((l) => ({
                range: {
                  startLineNumber: l.startLine + 1,
                  startColumn: l.startColumn + 1,
                  endLineNumber: l.endLine + 1,
                  endColumn: l.endColumn + 1,
                },
                url: l.target || undefined,
                tooltip: l.tooltip || undefined,
              })),
            };
          } catch {
            return { links: [] };
          }
        },
      }),
    );

    // Architecture C (prompt-1.md 498): Selection Range provider — expand/
    // shrink selection (textDocument/selectionRange)。Monaco 的
    // provideSelectionRanges 返回 SelectionRange[][]：外层数组每个元素对应
    // 一个 position，内层数组是从最内层到最外层的平坦链（Monaco 的
    // SelectionRange 接口只有 range，无 parent 字段；链通过数组顺序表达）。
    disposables.push(
      monaco.languages.registerSelectionRangeProvider(lang, {
        provideSelectionRanges: async (model, positions) => {
          const snapshot = captureLSPModelSnapshot(model, lang, preferredPath);
          if (!snapshot) return [];
          try {
            const { getLSPSelectionRanges } = await import("@/stores/lsp");
            if (model.isDisposed?.()) return [];
            const results: monacoEditor.languages.SelectionRange[][] = [];
            for (const pos of positions) {
              const ranges = await getLSPSelectionRanges(
                snapshot.lspLanguage,
                snapshot.filePath,
                pos.lineNumber - 1,
                pos.column - 1,
                snapshot.content,
              );
              if (model.isDisposed?.()) return [];
              // LSPSelectionRange 使用 parent 指针表达嵌套；Monaco 期望平坦
              // 数组（从最内层到最外层）。遍历 parent 链构建该数组。
              const chain: monacoEditor.languages.SelectionRange[] = [];
              let current = ranges.length ? ranges[0] : undefined;
              while (current) {
                chain.push({
                  range: {
                    startLineNumber: current.startLine + 1,
                    startColumn: current.startColumn + 1,
                    endLineNumber: current.endLine + 1,
                    endColumn: current.endColumn + 1,
                  },
                });
                current = current.parent;
              }
              results.push(chain);
            }
            return results;
          } catch {
            return [];
          }
        },
      }),
    );

    // Architecture C (prompt-1.md 493): Folding Range provider — code folding
    // regions (textDocument/foldingRange). Maps 0-based LSP lines to 1-based
    // Monaco lines. LSP string kinds are converted to Monaco FoldingRangeKind.
    disposables.push(
      monaco.languages.registerFoldingRangeProvider(lang, {
        provideFoldingRanges: async (model) => {
          const filePath = resolveModelFilePath(model, preferredPath);
          if (!filePath) return [];
          const lspLang = monacoLangToLSPKey(lang, filePath);
          if (!lspLang) return [];
          const { getLSPFoldingRanges } = await import("@/stores/lsp");
          const ranges = await getLSPFoldingRanges(
            lspLang,
            filePath,
            model.getValue(),
          );
          if (!ranges.length) return [];
          return ranges.map((r) => ({
            start: r.startLine + 1,
            end: r.endLine + 1,
            // LSP uses string kinds ("comment"/"imports"/"region"); Monaco
            // expects FoldingRangeKind instances. fromValue handles the
            // conversion; unknown kinds fall back to undefined (generic fold).
            kind: r.kind
              ? monaco.languages.FoldingRangeKind.fromValue(r.kind)
              : undefined,
          }));
        },
      }),
    );

    // F-8 (prompt-2.md 517-535): Color provider — 颜色块预览与拾取器
    // (textDocument/documentColor + textDocument/colorPresentation)。
    // 将 0-based LSP 位置/范围转换为 1-based Monaco 位置。
    // 重要约定（task-2.md 105-108）：后端无 server 运行时返回 error，
    // 前端 try/catch 降级返回空数组（Monaco 隐藏颜色装饰）。
    disposables.push(
      monaco.languages.registerColorProvider(lang, {
        provideDocumentColors: async (model) => {
          const filePath = resolveModelFilePath(model, preferredPath);
          if (!filePath) return [];
          const lspLang = monacoLangToLSPKey(lang, filePath);
          if (!lspLang) return [];
          const uri = monaco.Uri.file(filePath).toString();
          const { lspService } = await import("@/api/services");
          let colors: import("@/types").ColorInformation[] = [];
          try {
            colors = await lspService.getDocumentColors(uri);
          } catch {
            // 后端无 server 或 RPC 失败 — 降级为无颜色装饰
            return [];
          }
          if (!colors.length) return [];
          return colors.map((c) => ({
            range: {
              startLineNumber: c.range.start.line + 1,
              startColumn: c.range.start.character + 1,
              endLineNumber: c.range.end.line + 1,
              endColumn: c.range.end.character + 1,
            },
            color: c.color,
          }));
        },
        provideColorPresentations: async (model, colorInfo) => {
          const filePath = resolveModelFilePath(model, preferredPath);
          if (!filePath) return [];
          const lspLang = monacoLangToLSPKey(lang, filePath);
          if (!lspLang) return [];
          const uri = monaco.Uri.file(filePath).toString();
          // Monaco colorInfo.range 是 1-based；后端需要 0-based LSPRange
          const range: import("@/types").LSPRange = {
            start: {
              line: colorInfo.range.startLineNumber - 1,
              character: colorInfo.range.startColumn - 1,
            },
            end: {
              line: colorInfo.range.endLineNumber - 1,
              character: colorInfo.range.endColumn - 1,
            },
          };
          const { lspService } = await import("@/api/services");
          let presentations: import("@/types").ColorPresentation[] = [];
          try {
            presentations = await lspService.getColorPresentations(
              uri,
              colorInfo.color,
              range,
            );
          } catch {
            return [];
          }
          if (!presentations.length) return [];
          return presentations.map((p) => {
            const out: monacoEditor.languages.IColorPresentation = {
              label: p.label,
            };
            if (p.textEdit) {
              out.textEdit = {
                range: {
                  startLineNumber: p.textEdit.startLine + 1,
                  startColumn: p.textEdit.startCol + 1,
                  endLineNumber: p.textEdit.endLine + 1,
                  endColumn: p.textEdit.endCol + 1,
                },
                text: p.textEdit.newText,
              };
            }
            if (p.additionalTextEdits?.length) {
              out.additionalTextEdits = p.additionalTextEdits.map((e) => ({
                range: {
                  startLineNumber: e.startLine + 1,
                  startColumn: e.startCol + 1,
                  endLineNumber: e.endLine + 1,
                  endColumn: e.endCol + 1,
                },
                text: e.newText,
              }));
            }
            return out;
          });
        },
      }),
    );

    // F-8 (prompt-2.md 517-535): Linked Editing Range provider — 同步编辑
    // (如 HTML 起始/结束标签，textDocument/prepareLinkedEdits)。
    // Monaco LinkedEditingRanges = { ranges: IRange[] }（1-based）；后端返回
    // LinkedEditRange[]（0-based LSPRange），需转换并包装。
    // 注意 Monaco API 名为 provideLinkedEditingRanges（非 provideLinkedRanges）。
    disposables.push(
      monaco.languages.registerLinkedEditingRangeProvider(lang, {
        provideLinkedEditingRanges: async (model, position) => {
          const filePath = resolveModelFilePath(model, preferredPath);
          if (!filePath) return { ranges: [] };
          const lspLang = monacoLangToLSPKey(lang, filePath);
          if (!lspLang) return { ranges: [] };
          const uri = monaco.Uri.file(filePath).toString();
          const { lspService } = await import("@/api/services");
          let ranges: import("@/types").LinkedEditRange[] = [];
          try {
            ranges = await lspService.prepareLinkedEdits(uri, {
              line: position.lineNumber - 1,
              character: position.column - 1,
            });
          } catch {
            return { ranges: [] };
          }
          if (!ranges.length) return { ranges: [] };
          return {
            ranges: ranges.map((r) => ({
              startLineNumber: r.range.start.line + 1,
              startColumn: r.range.start.character + 1,
              endLineNumber: r.range.end.line + 1,
              endColumn: r.range.end.character + 1,
            })),
          };
        },
      }),
    );
  }

  // 高级 LSP Provider 独立维护映射和竞态保护；父级统一持有 disposable。
  for (const lang of languages) {
    disposables.push(
      registerSemanticTokensProvider(monaco, lang, preferredPath),
      registerInlayHintsProvider(monaco, lang, preferredPath),
      registerCodeLensProvider(monaco, lang, preferredPath),
    );
  }

  // G-COMP-04: Snippet completions for common patterns (Go, TS, JS).
  // These provide quick templates (for/if/func etc.) that work even without
  // an LSP server running, improving the out-of-box completion experience.
  for (const [lang, snippets] of Object.entries(SNIPPETS)) {
    if (!languages.includes(lang as (typeof languages)[number])) continue;
    disposables.push(
      monaco.languages.registerCompletionItemProvider(lang, {
        triggerCharacters: [""],
        provideCompletionItems: (_model, position) => {
          const word = _model.getWordUntilPosition(position);
          if (!word.word) return { suggestions: [] };
          const range = {
            startLineNumber: position.lineNumber,
            endLineNumber: position.lineNumber,
            startColumn: word.startColumn,
            endColumn: word.endColumn,
          };
          return {
            suggestions: snippets.map((s) => ({
              label: s.label,
              kind: monaco.languages.CompletionItemKind.Snippet,
              insertText: s.insertText,
              insertTextRules:
                monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet,
              detail: s.detail,
              documentation: s.description,
              sortText: "z_" + s.label,
              range,
            })),
          };
        },
      }),
    );
  }

  // 10C: gopls completion remains registered above; this provider only adds
  // contextual fallback snippets for Go struct-tag literals.
  disposables.push(
    monaco.languages.registerCompletionItemProvider("go", {
      triggerCharacters: ["`", '"', ",", " "],
      provideCompletionItems: (model, position) => {
        const suggestions = getGoStructTagSuggestions(
          model.getLineContent(position.lineNumber),
          position.column,
        );
        const range = {
          startLineNumber: position.lineNumber,
          endLineNumber: position.lineNumber,
          startColumn: position.column,
          endColumn: position.column,
        };
        return {
          suggestions: suggestions.map((suggestion) => ({
            label: suggestion.label,
            kind: monaco.languages.CompletionItemKind.Snippet,
            insertText: suggestion.insertText,
            insertTextRules:
              monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet,
            detail: suggestion.detail,
            sortText: `0_tag_${suggestion.label}`,
            range,
          })),
        };
      },
    }),
  );

  // G-HL-04: Document highlight provider — highlights all occurrences of the
  // word under the cursor. Uses simple word matching (no LSP required) for
  // instant feedback, matching VSCode's default document highlight behavior.
  for (const lang of languages) {
    disposables.push(
      monaco.languages.registerDocumentHighlightProvider(lang, {
        provideDocumentHighlights: (model, position) => {
          const word = model.getWordAtPosition(position);
          if (!word) return [];
          // M-22: Limit the scan to a bounded window around the cursor
          // (±HIGHLIGHT_SCAN_WINDOW lines) instead of the whole document.
          // The editor viewport isn't accessible here, so the cursor window
          // is the fallback. For small documents the range covers everything.
          const scanRange = computeDocumentHighlightScanRange(
            model.getLineCount(),
            position.lineNumber,
            HIGHLIGHT_SCAN_WINDOW,
          );
          const matches = model.findMatches(
            word.word,
            scanRange,
            false,
            true,
            null,
            true,
          );
          return matches
            .filter(
              (m) =>
                m.range.startLineNumber !== position.lineNumber ||
                m.range.startColumn !== position.column,
            )
            .map((m) => ({
              range: m.range,
              kind: monaco.languages.DocumentHighlightKind.Read,
            }));
        },
      }),
    );
  }

  const providerDisposable: monacoEditor.IDisposable = {
    dispose() {
      lifecycle.active = false;
      lifecycle.triggerGenerations.clear();
      lifecycle.triggerLoaded.clear();
      lifecycle.triggerCharactersByServer.clear();
      for (const d of disposables) {
        try {
          d.dispose();
        } catch {
          /* already disposed */
        }
      }
    },
  };
  const registration: SharedLSPProviderRegistration = {
    references: 1,
    disposable: providerDisposable,
  };
  sharedLSPProviderRegistrations.set(monaco, registration);
  return createProviderRegistrationLease(monaco, registration);
}

/**
 * Apply a list of LSP text edits to a full document string (0-based lines/cols).
 * Applies from end-of-document backwards so offsets stay valid.
 */
export function applyTextEditsToContent(
  content: string,
  edits: Array<{
    startLine: number;
    startCol: number;
    endLine: number;
    endCol: number;
    newText: string;
  }>,
): string {
  if (!edits.length) return content;
  const lines = content.split("\n");
  const offsetAt = (line: number, col: number): number => {
    let o = 0;
    const max = Math.min(line, lines.length);
    for (let i = 0; i < max; i++) o += lines[i].length + 1; // +1 newline
    return o + Math.max(0, col);
  };
  const sorted = [...edits].sort((a, b) => {
    const oa = offsetAt(a.startLine, a.startCol);
    const ob = offsetAt(b.startLine, b.startCol);
    return ob - oa;
  });
  let result = content;
  for (const e of sorted) {
    const start = offsetAt(e.startLine, e.startCol);
    const end = offsetAt(e.endLine, e.endCol);
    result =
      result.slice(0, start) + e.newText + result.slice(Math.max(start, end));
  }
  return result;
}

/**
 * Apply LSP format edits to the open buffer (Format Document / Format on Save).
 */
export async function formatActiveDocument(
  language: string,
  filePath: string,
  content: string,
): Promise<boolean> {
  const lspLang = monacoLangToLSPKey(language, filePath) ?? language;
  if (
    !lspLang ||
    !["go", "typescript", "javascript", "json", "css", "html", "yaml"].includes(
      lspLang,
    )
  )
    return false;
  const edits = await formatLSPDocument(lspLang, filePath, content);
  if (!edits.length) return false;
  const next = applyTextEditsToContent(content, edits);
  if (next === content) return false;
  const currentContent = editorState.openFiles.find(
    (file) => file.path === filePath,
  )?.content;
  if (currentContent !== undefined && currentContent !== content) {
    // 格式化响应基于旧 buffer，不能覆盖用户在等待期间的新输入。
    return false;
  }
  updateContent(filePath, next);
  return true;
}

/**
 * Apply multi-file rename edits (prompt-9 9-B / 11-C). Opens files as needed via updateContent/openFile.
 */
export async function applyWorkspaceEdits(
  files: Array<{
    filePath: string;
    edits: Array<{
      startLine: number;
      startCol: number;
      endLine: number;
      endCol: number;
      newText: string;
    }>;
  }>,
): Promise<number> {
  const { applied } = await applyWorkspaceEditsDetailed(files);
  return applied;
}

/** prompt-11 11-C: returns applied count + failed paths for Output. */
export async function applyWorkspaceEditsDetailed(
  files: Array<{
    filePath: string;
    edits: Array<{
      startLine: number;
      startCol: number;
      endLine: number;
      endCol: number;
      newText: string;
    }>;
  }>,
): Promise<{ applied: number; failed: string[] }> {
  let applied = 0;
  const failed: string[] = [];
  const { openFileFromPath } = await import("@/stores/editor");
  const { fileService } = await import("@/api/services");
  for (const f of files) {
    if (!f.edits?.length) continue;
    try {
      let content = editorState.openFiles.find(
        (o) => o.path === f.filePath,
      )?.content;
      if (content == null) {
        content = await fileService.readFile(f.filePath);
        await openFileFromPath(f.filePath);
      }
      const next = applyTextEditsToContent(content, f.edits);
      if (updateContent(f.filePath, next)) applied += 1;
      else failed.push(`${f.filePath}: updateContent failed`);
    } catch (e) {
      failed.push(
        `${f.filePath}: ${e instanceof Error ? e.message : String(e)}`,
      );
    }
  }
  return { applied, failed };
}

export { closeLSPDocument };
