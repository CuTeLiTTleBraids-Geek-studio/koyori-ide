// Koyori IDE 模块 · Lsp。
// 喵，这是 Koyori IDE 的 Lsp 模块（前端实现）~
import * as LSPServiceBindings from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/lspservice.js";
import type {
  InlayHint as BindingInlayHint,
  SemanticToken as BindingSemanticToken,
} from "../../bindings/github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide/services/models.js";
import type {
  InlayHint, LSPCompletionItem, LSPCompletionRequest, LSPCompletionTextEdit,
  LSPDocumentSymbol, LSPRange, LSPServerStatus, LSPTextEdit, SemanticToken,
} from "@/types";
import {
  isRecord, optionalBoolean, optionalInteger, optionalNumberArray,
  optionalString, optionalStringArray, requiredInteger, requiredString,
  unwrapNullable, warnInvalidBoundaryValue,
} from "./boundary";

type BindingLSPServerStatus = NonNullable<
  Awaited<ReturnType<typeof LSPServiceBindings.DetectLSPServers>>
>[number];
type BindingLSPFileTextEdits = NonNullable<
  Awaited<ReturnType<typeof LSPServiceBindings.RenameSymbolWorkspace>>
>[number];
type BindingLSPDocumentSymbol = NonNullable<
  Awaited<ReturnType<typeof LSPServiceBindings.GetDocumentSymbols>>
>[number];
type BindingLSPCodeAction = NonNullable<
  Awaited<ReturnType<typeof LSPServiceBindings.GetCodeActions>>
>[number];
type NormalizedLSPFileTextEdits = Omit<BindingLSPFileTextEdits, "edits"> & {
  edits: NonNullable<BindingLSPFileTextEdits["edits"]>;
};
type BindingWorkspaceEditPreview = NonNullable<BindingLSPCodeAction["preview"]>;
type BindingWorkspaceEditPreviewFile = NonNullable<
  BindingWorkspaceEditPreview["files"]
>[number];
type NormalizedWorkspaceEditPreviewFile = Omit<
  BindingWorkspaceEditPreviewFile,
  "version"
> & { version?: number };
export type NormalizedLSPCodeAction = Omit<
  BindingLSPCodeAction,
  "commandArguments" | "edit" | "preview"
> & {
  commandArguments?: NonNullable<BindingLSPCodeAction["commandArguments"]>;
  edit?: NormalizedLSPFileTextEdits[] | null;
  preview?: Omit<BindingWorkspaceEditPreview, "files"> & {
    files: NormalizedWorkspaceEditPreviewFile[];
  };
};
type BindingLSPSignatureHelp = NonNullable<
  Awaited<ReturnType<typeof LSPServiceBindings.GetSignatureHelp>>
>;
type BindingLSPSelectionRange = NonNullable<
  Awaited<ReturnType<typeof LSPServiceBindings.GetSelectionRanges>>
>[number];
type BindingLSPIncomingCall = NonNullable<
  Awaited<ReturnType<typeof LSPServiceBindings.CallHierarchyIncomingCalls>>
>[number];
type BindingLSPOutgoingCall = NonNullable<
  Awaited<ReturnType<typeof LSPServiceBindings.CallHierarchyOutgoingCalls>>
>[number];
type BindingLSPColorPresentation = NonNullable<
  Awaited<ReturnType<typeof LSPServiceBindings.GetColorPresentations>>
>[number];

function normalizeLSPServerStatus(status: BindingLSPServerStatus): LSPServerStatus {
  const framework = status.framework;
  return {
    ...status,
    framework:
      framework === "vue" || framework === "angular" || framework === "react"
        ? framework
        : undefined,
  };
}

function normalizeLSPFileTextEdits(
  file: BindingLSPFileTextEdits,
): NormalizedLSPFileTextEdits {
  return {
    filePath: file.filePath,
    edits: file.edits ?? [],
  };
}

function normalizeLSPDocumentSymbol(
  symbol: BindingLSPDocumentSymbol,
): LSPDocumentSymbol {
  return {
    ...symbol,
    children: symbol.children?.map(normalizeLSPDocumentSymbol) ?? undefined,
  };
}

function normalizeLSPCodeAction(
  action: BindingLSPCodeAction,
): NormalizedLSPCodeAction {
  return {
    ...action,
    commandArguments: action.commandArguments ?? undefined,
    edit:
      action.edit === null
        ? null
        : action.edit?.map(normalizeLSPFileTextEdits),
    preview: action.preview
      ? {
          ...action.preview,
          files: (action.preview.files ?? []).map((file) => ({
            ...file,
            version: file.version ?? undefined,
          })),
        }
      : undefined,
  };
}

function normalizeLSPSignatureHelp(
  help: BindingLSPSignatureHelp | null,
) {
  if (!help) return null;
  return {
    ...help,
    parameters: help.parameters ?? [],
  };
}

function normalizeLSPSelectionRange(
  range: BindingLSPSelectionRange,
): import("@/types").LSPSelectionRange {
  return {
    ...range,
    parent: range.parent
      ? normalizeLSPSelectionRange(range.parent)
      : undefined,
  };
}

function normalizeLSPIncomingCall(
  call: BindingLSPIncomingCall,
): import("@/types").LSPCallHierarchyIncomingCall {
  return {
    ...call,
    fromRanges: call.fromRanges ?? [],
  };
}

function normalizeLSPOutgoingCall(
  call: BindingLSPOutgoingCall,
): import("@/types").LSPCallHierarchyOutgoingCall {
  return {
    ...call,
    fromRanges: call.fromRanges ?? [],
  };
}

function normalizeLSPColorPresentation(
  presentation: BindingLSPColorPresentation,
): import("@/types").ColorPresentation {
  return {
    ...presentation,
    textEdit: presentation.textEdit ?? undefined,
    additionalTextEdits: presentation.additionalTextEdits ?? undefined,
  };
}

type BindingLSPCompletionItem = Parameters<
  typeof LSPServiceBindings.ResolveCompletionItem
>[1];
type BindingLSPCompletionTextEdit = NonNullable<BindingLSPCompletionItem["textEdit"]>;

const ZERO_LSP_RANGE: LSPRange = {
  start: { line: 0, character: 0 },
  end: { line: 0, character: 0 },
};

function normalizeLSPRange(value: unknown, path: string): LSPRange | undefined {
  if (value === undefined || value === null) return undefined;
  if (!isRecord(value) || !isRecord(value.start) || !isRecord(value.end)) {
    warnInvalidBoundaryValue(path, "an LSP range", "undefined");
    return undefined;
  }
  return {
    start: {
      line: requiredInteger(value.start.line, `${path}.start.line`, 0),
      character: requiredInteger(value.start.character, `${path}.start.character`, 0),
    },
    end: {
      line: requiredInteger(value.end.line, `${path}.end.line`, 0),
      character: requiredInteger(value.end.character, `${path}.end.character`, 0),
    },
  };
}

function toBindingLSPTextEdit(
  value: unknown,
  path: string,
): BindingLSPCompletionTextEdit {
  const edit = isRecord(value) ? value : {};
  if (!isRecord(value)) {
    warnInvalidBoundaryValue(path, "an LSP text edit", "an empty edit");
  }
  const range = normalizeLSPRange(edit.range, `${path}.range`);
  const insert = normalizeLSPRange(edit.insert, `${path}.insert`);
  const replace = normalizeLSPRange(edit.replace, `${path}.replace`);
  const coordinateRange = replace ?? range ?? insert;
  const startLine = optionalInteger(edit.startLine, `${path}.startLine`);
  const startCol = optionalInteger(edit.startCol, `${path}.startCol`);
  const endLine = optionalInteger(edit.endLine, `${path}.endLine`);
  const endCol = optionalInteger(edit.endCol, `${path}.endCol`);
  if (
    startLine === undefined &&
    startCol === undefined &&
    endLine === undefined &&
    endCol === undefined &&
    !coordinateRange
  ) {
    warnInvalidBoundaryValue(path, "an LSP text edit with a range", "a zero range");
  }
  return {
    startLine: startLine ?? coordinateRange?.start.line ?? 0,
    startCol: startCol ?? coordinateRange?.start.character ?? 0,
    endLine: endLine ?? coordinateRange?.end.line ?? 0,
    endCol: endCol ?? coordinateRange?.end.character ?? 0,
    range,
    insert,
    replace,
    newText: requiredString(edit.newText, `${path}.newText`),
  };
}

function fromBindingLSPTextEdit(
  value: unknown,
  path: string,
): LSPCompletionTextEdit {
  const edit = toBindingLSPTextEdit(value, path);
  return {
    startLine: edit.startLine,
    startCol: edit.startCol,
    endLine: edit.endLine,
    endCol: edit.endCol,
    range: edit.range ?? undefined,
    insert: edit.insert ?? undefined,
    replace: edit.replace ?? undefined,
    newText: edit.newText,
  };
}

function normalizeLSPDocumentation(
  value: unknown,
  path: string,
): LSPCompletionItem["documentation"] {
  if (value === undefined || value === null || typeof value === "string") return value;
  if (isRecord(value) && typeof value.kind === "string" && typeof value.value === "string") {
    return { kind: value.kind, value: value.value };
  }
  warnInvalidBoundaryValue(path, "a string or MarkupContent object", "undefined");
  return undefined;
}

function normalizeLSPLabelDetails(
  value: unknown,
  path: string,
): LSPCompletionItem["labelDetails"] {
  if (value === undefined || value === null) return undefined;
  if (!isRecord(value)) {
    warnInvalidBoundaryValue(path, "a completion label-details object", "undefined");
    return undefined;
  }
  return {
    detail: optionalString(value.detail, `${path}.detail`),
    description: optionalString(value.description, `${path}.description`),
  };
}

function normalizeLSPTextEditArray<T>(
  value: unknown,
  path: string,
  convert: (edit: unknown, path: string) => T,
): T[] | undefined {
  if (value === undefined || value === null) return undefined;
  if (!Array.isArray(value)) {
    warnInvalidBoundaryValue(path, "an array of LSP text edits", "undefined");
    return undefined;
  }
  return value.map((edit, index) => convert(edit, `${path}[${index}]`));
}

/** Convert a frontend completion item into the exact Wails resolve payload. */
export function toBindingLSPCompletionItem(
  item: LSPCompletionItem,
): BindingLSPCompletionItem {
  return {
    label: requiredString(item.label, "LSPCompletionItem.label"),
    kind: requiredInteger(item.kind, "LSPCompletionItem.kind", 0),
    detail: requiredString(item.detail, "LSPCompletionItem.detail"),
    insertText: optionalString(item.insertText, "LSPCompletionItem.insertText"),
    textEditText: optionalString(item.textEditText, "LSPCompletionItem.textEditText"),
    insertTextFormat: optionalInteger(
      item.insertTextFormat,
      "LSPCompletionItem.insertTextFormat",
    ),
    insertTextMode: optionalInteger(item.insertTextMode, "LSPCompletionItem.insertTextMode"),
    sortText: optionalString(item.sortText, "LSPCompletionItem.sortText"),
    filterText: optionalString(item.filterText, "LSPCompletionItem.filterText"),
    preselect: optionalBoolean(item.preselect, "LSPCompletionItem.preselect"),
    deprecated: optionalBoolean(item.deprecated, "LSPCompletionItem.deprecated"),
    tags: optionalNumberArray(item.tags, "LSPCompletionItem.tags"),
    documentation: normalizeLSPDocumentation(
      item.documentation,
      "LSPCompletionItem.documentation",
    ),
    data: item.data,
    commitCharacters: optionalStringArray(
      item.commitCharacters,
      "LSPCompletionItem.commitCharacters",
    ),
    textEdit: item.textEdit
      ? toBindingLSPTextEdit(item.textEdit, "LSPCompletionItem.textEdit")
      : undefined,
    labelDetails: normalizeLSPLabelDetails(
      item.labelDetails,
      "LSPCompletionItem.labelDetails",
    ),
    additionalEdits: normalizeLSPTextEditArray(
      item.additionalEdits,
      "LSPCompletionItem.additionalEdits",
      toBindingLSPTextEdit,
    ),
  };
}

/** Convert generated completion data into the frontend's protocol-aware DTO. */
export function fromBindingLSPCompletionItem(
  item: BindingLSPCompletionItem,
  path = "LSPCompletionItem",
): LSPCompletionItem {
  return {
    label: requiredString(item.label, `${path}.label`),
    kind: requiredInteger(item.kind, `${path}.kind`, 0),
    detail: requiredString(item.detail, `${path}.detail`),
    insertText: optionalString(item.insertText, `${path}.insertText`),
    textEditText: optionalString(item.textEditText, `${path}.textEditText`),
    insertTextFormat: optionalInteger(item.insertTextFormat, `${path}.insertTextFormat`),
    insertTextMode: optionalInteger(item.insertTextMode, `${path}.insertTextMode`),
    sortText: optionalString(item.sortText, `${path}.sortText`),
    filterText: optionalString(item.filterText, `${path}.filterText`),
    preselect: optionalBoolean(item.preselect, `${path}.preselect`),
    deprecated: optionalBoolean(item.deprecated, `${path}.deprecated`),
    tags: optionalNumberArray(item.tags, `${path}.tags`),
    documentation: normalizeLSPDocumentation(item.documentation, `${path}.documentation`),
    data: item.data,
    commitCharacters: optionalStringArray(item.commitCharacters, `${path}.commitCharacters`),
    textEdit: item.textEdit
      ? fromBindingLSPTextEdit(item.textEdit, `${path}.textEdit`)
      : undefined,
    labelDetails: normalizeLSPLabelDetails(item.labelDetails, `${path}.labelDetails`),
    additionalEdits: normalizeLSPTextEditArray(
      item.additionalEdits,
      `${path}.additionalEdits`,
      (edit, editPath): LSPTextEdit => {
        const normalized = fromBindingLSPTextEdit(edit, editPath);
        if (
          "startLine" in normalized &&
          "startCol" in normalized &&
          "endLine" in normalized &&
          "endCol" in normalized
        ) {
          return {
            startLine: normalized.startLine,
            startCol: normalized.startCol,
            endLine: normalized.endLine,
            endCol: normalized.endCol,
            newText: normalized.newText,
          };
        }
        return {
          startLine: ZERO_LSP_RANGE.start.line,
          startCol: ZERO_LSP_RANGE.start.character,
          endLine: ZERO_LSP_RANGE.end.line,
          endCol: ZERO_LSP_RANGE.end.character,
          newText: normalized.newText,
        };
      },
    ),
  };
}

export function fromBindingSemanticToken(
  token: BindingSemanticToken,
  path = "SemanticToken",
): SemanticToken {
  let modifiers = 0;
  for (const modifier of token.modifiers ?? []) {
    if (!Number.isInteger(modifier) || modifier < 0 || modifier >= 32) {
      warnInvalidBoundaryValue(
        `${path}.modifiers`,
        "integer indices between 0 and 31",
        "the valid modifier subset",
      );
      continue;
    }
    modifiers = (modifiers | (1 << modifier)) >>> 0;
  }
  return {
    line: requiredInteger(token.line, `${path}.line`, 0),
    start: requiredInteger(token.column, `${path}.column`, 0),
    length: requiredInteger(token.length, `${path}.length`, 0),
    type: requiredInteger(token.type, `${path}.type`, 0),
    modifiers,
  };
}

function normalizeInlayHintTooltip(
  value: unknown,
  path: string,
): string | undefined {
  if (value === undefined || value === null) return undefined;
  if (typeof value === "string") return value || undefined;
  if (isRecord(value) && typeof value.value === "string") {
    return value.value || undefined;
  }
  warnInvalidBoundaryValue(path, "a string or MarkupContent object", "undefined");
  return undefined;
}

function toBusinessLSPTextEdit(value: unknown, path: string): LSPTextEdit {
  const edit = fromBindingLSPTextEdit(value, path);
  if ("startLine" in edit) {
    return {
      startLine: edit.startLine,
      startCol: edit.startCol,
      endLine: edit.endLine,
      endCol: edit.endCol,
      newText: edit.newText,
    };
  }
  let range: LSPRange;
  if ("range" in edit) {
    range = edit.range;
  } else if ("replace" in edit) {
    range = edit.replace;
  } else {
    range = ZERO_LSP_RANGE;
  }
  return {
    startLine: range.start.line,
    startCol: range.start.character,
    endLine: range.end.line,
    endCol: range.end.character,
    newText: edit.newText,
  };
}

export function fromBindingInlayHint(
  hint: BindingInlayHint,
  path = "InlayHint",
): InlayHint {
  const normalized: InlayHint = {
    position: {
      line: requiredInteger(hint.line, `${path}.line`, 0),
      character: requiredInteger(hint.column, `${path}.column`, 0),
    },
    label: requiredString(hint.label, `${path}.label`),
    kind: requiredInteger(hint.kind, `${path}.kind`, 0),
  };
  const tooltip = normalizeInlayHintTooltip(hint.tooltip, `${path}.tooltip`);
  if (tooltip !== undefined) normalized.tooltip = tooltip;
  if (hint.paddingLeft !== undefined) {
    normalized.paddingLeft = optionalBoolean(hint.paddingLeft, `${path}.paddingLeft`);
  }
  if (hint.paddingRight !== undefined) {
    normalized.paddingRight = optionalBoolean(hint.paddingRight, `${path}.paddingRight`);
  }
  if (hint.textEdits?.length) {
    normalized.textEdits = hint.textEdits.map((edit, index) =>
      toBusinessLSPTextEdit(edit, `${path}.textEdits[${index}]`));
  }
  if (hint.data !== undefined) normalized.data = hint.data;
  return normalized;
}

export const lspService = {
  // G-FEAT-02 / prompt-8: gopls + typescript-language-server / vtsls.
  detectServers: async () =>
    (await unwrapNullable(LSPServiceBindings.DetectLSPServers(), [])).map(
      normalizeLSPServerStatus,
    ),
  startServer: (language: string) =>
    LSPServiceBindings.StartLSPServer(language) as Promise<void>,
  stopServer: (language: string) =>
    LSPServiceBindings.StopLSPServer(language) as Promise<void>,
  getCompletions: async (req: LSPCompletionRequest) =>
    (await unwrapNullable(LSPServiceBindings.GetCompletions(req), [])).map(
      (item, index) => fromBindingLSPCompletionItem(item, `LSPCompletionItems[${index}]`),
    ),
  getHover: (req: LSPCompletionRequest) =>
    LSPServiceBindings.GetHover(req) as Promise<string>,
  getDiagnostics: (req: LSPCompletionRequest) =>
    unwrapNullable(LSPServiceBindings.GetDiagnostics(req), []),
  // prompt-8 M1/M2
  getDefinition: (req: LSPCompletionRequest) =>
    unwrapNullable(LSPServiceBindings.GetDefinition(req), []),
  getReferences: (req: LSPCompletionRequest) =>
    unwrapNullable(LSPServiceBindings.GetReferences(req), []),
  formatDocument: (req: LSPCompletionRequest) =>
    unwrapNullable(LSPServiceBindings.FormatDocument(req), []),
  renameSymbol: (req: LSPCompletionRequest, newName: string) =>
    unwrapNullable(LSPServiceBindings.RenameSymbol(req, newName), []),
  renameSymbolWorkspace: async (req: LSPCompletionRequest, newName: string) =>
    (await unwrapNullable(
      LSPServiceBindings.RenameSymbolWorkspace(req, newName),
      [],
    )).map(normalizeLSPFileTextEdits),
  getSignatureHelp: async (req: LSPCompletionRequest) =>
    normalizeLSPSignatureHelp(await LSPServiceBindings.GetSignatureHelp(req)),
  // G-HL-02: Code Lens support — shows reference counts, implementations, etc.
  getCodeLenses: (req: LSPCompletionRequest) =>
    unwrapNullable(LSPServiceBindings.GetCodeLenses(req), []),
  // G-ACT-01: Code actions (lightbulb / quick fixes / refactors).
  getCodeActions: async (req: LSPCompletionRequest) =>
    (await unwrapNullable(LSPServiceBindings.GetCodeActions(req), [])).map(
      normalizeLSPCodeAction,
    ),
  resolveCodeAction: async (
    req: LSPCompletionRequest,
    action: ReturnType<typeof normalizeLSPCodeAction>,
  ) => normalizeLSPCodeAction(
    await LSPServiceBindings.ResolveCodeAction(req, action),
  ),
  // G-ACT-02: Go to Implementation (textDocument/implementation).
  getImplementation: (req: LSPCompletionRequest) =>
    unwrapNullable(LSPServiceBindings.GetImplementation(req), []),
  organizeImports: (req: LSPCompletionRequest) =>
    unwrapNullable(LSPServiceBindings.OrganizeImports(req), []),
  // G-COMP-02: document outline (textDocument/documentSymbol).
  getDocumentSymbols: async (req: LSPCompletionRequest) =>
    (await unwrapNullable(LSPServiceBindings.GetDocumentSymbols(req), [])).map(
      normalizeLSPDocumentSymbol,
    ),
  // G-COMP-02: workspace symbol search (workspace/symbol, Ctrl+T).
  getWorkspaceSymbols: (language: string, query: string) =>
    unwrapNullable(LSPServiceBindings.GetWorkspaceSymbols(language, query), []),
  // G-COMP-02: semantic tokens for highlighting (textDocument/semanticTokens/full).
  getSemanticTokens: async (req: LSPCompletionRequest) =>
    (await unwrapNullable(LSPServiceBindings.GetSemanticTokens(req), [])).map(
      (token, index) => fromBindingSemanticToken(token, `SemanticTokens[${index}]`),
    ),
  // Priority 1: inlay hints (textDocument/inlayHint) — inline type/parameter annotations.
  getInlayHints: async (req: LSPCompletionRequest) =>
    (await unwrapNullable(LSPServiceBindings.GetInlayHints(req), [])).map(
      (hint, index) => fromBindingInlayHint(hint, `InlayHints[${index}]`),
    ),
  // G-COMP-02: resolve additional details (documentation/detail) for a completion item.
  resolveCompletionItem: async (language: string, item: LSPCompletionItem) =>
    fromBindingLSPCompletionItem(
      await LSPServiceBindings.ResolveCompletionItem(
        language,
        toBindingLSPCompletionItem(item),
      ),
    ),
  getCallStatus: (language: string) =>
    LSPServiceBindings.GetCallStatus(language) as Promise<{
      language: string;
      code: string;
      message: string;
    }>,
  closeDocument: (language: string, filePath: string) =>
    LSPServiceBindings.CloseDocument(language, filePath) as Promise<void>,
  didSaveDocument: (req: LSPCompletionRequest) =>
    LSPServiceBindings.DidSaveDocument(req) as Promise<void>,
  // Architecture C (prompt-1.md 491-500): additional LSP providers.
  getDeclaration: (req: LSPCompletionRequest) =>
    unwrapNullable(LSPServiceBindings.GetDeclaration(req), []),
  getTypeDefinition: (req: LSPCompletionRequest) =>
    unwrapNullable(LSPServiceBindings.GetTypeDefinition(req), []),
  getDocumentLinks: (req: LSPCompletionRequest) =>
    unwrapNullable(LSPServiceBindings.GetDocumentLinks(req), []),
  getSelectionRanges: async (req: LSPCompletionRequest) =>
    (await unwrapNullable(LSPServiceBindings.GetSelectionRanges(req), [])).map(
      normalizeLSPSelectionRange,
    ),
  getFoldingRanges: (req: LSPCompletionRequest) =>
    unwrapNullable(LSPServiceBindings.GetFoldingRanges(req), []),
  // F-1 (prompt-2.md): Call Hierarchy / Type Hierarchy.
  prepareCallHierarchy: (req: LSPCompletionRequest) =>
    unwrapNullable(LSPServiceBindings.PrepareCallHierarchy(req), []),
  callHierarchyIncomingCalls: async (req: LSPCompletionRequest, item: import("@/types").LSPCallHierarchyItem) =>
    (await unwrapNullable(
      LSPServiceBindings.CallHierarchyIncomingCalls(req, item),
      [],
    )).map(normalizeLSPIncomingCall),
  callHierarchyOutgoingCalls: async (req: LSPCompletionRequest, item: import("@/types").LSPCallHierarchyItem) =>
    (await unwrapNullable(
      LSPServiceBindings.CallHierarchyOutgoingCalls(req, item),
      [],
    )).map(normalizeLSPOutgoingCall),
  prepareTypeHierarchy: (req: LSPCompletionRequest) =>
    unwrapNullable(LSPServiceBindings.PrepareTypeHierarchy(req), []),
  typeHierarchySupertypes: (req: LSPCompletionRequest, item: import("@/types").LSPTypeHierarchyItem) =>
    unwrapNullable(LSPServiceBindings.TypeHierarchySupertypes(req, item), []),
  typeHierarchySubtypes: (req: LSPCompletionRequest, item: import("@/types").LSPTypeHierarchyItem) =>
    unwrapNullable(LSPServiceBindings.TypeHierarchySubtypes(req, item), []),
  // F-2 (prompt-2.md): 注入 per-section LSP 配置（如 "gopls" / "typescript"），
  // 使 workspace/configuration 请求能返回对应配置给语言服务器。
  setLSPConfig: (section: string, config: unknown) =>
    LSPServiceBindings.SetLSPConfig(section, config),
  // F-8 (prompt-2.md 517-535): LSP colorProvider / linkedEditingRange.
  // 重要约定（task-2.md 105-108）：无 LSP server 运行时后端返回 error，
  // 调用方需 try/catch 并降级。
  getDocumentColors: (uri: string) =>
    unwrapNullable(LSPServiceBindings.GetDocumentColors(uri), []),
  getColorPresentations: (
    uri: string,
    color: import("@/types").Color,
    range: import("@/types").LSPRange,
  ) =>
    unwrapNullable(
      LSPServiceBindings.GetColorPresentations(uri, color, range),
      [],
    ).then((presentations) => presentations.map(normalizeLSPColorPresentation)),
  prepareLinkedEdits: (uri: string, position: import("@/types").LSPPosition) =>
    unwrapNullable(LSPServiceBindings.PrepareLinkedEdits(uri, position), []),
};
